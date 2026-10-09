#!/usr/bin/env python3
"""Prove actual log retrieval, stored traces, panel provisioning and alert reception."""
import json
import http.cookiejar
from html.parser import HTMLParser
import ssl
from urllib.error import HTTPError
from urllib.request import build_opener, HTTPCookieProcessor, HTTPSHandler, ProxyHandler, Request
from pathlib import Path
import subprocess
import time
from urllib.parse import urlencode

root = Path(__file__).resolve().parents[2]
deploy = root / 'deploy/event-platform'
compose = ['docker', 'compose', '-f', 'compose.yaml', '-f', '../../tests/event-platform/compose.yaml', '--profile', 'observability']

def get(url):
    code = 'import urllib.request; print(urllib.request.urlopen(' + repr(url) + ', timeout=10).read().decode())'
    result = subprocess.run(compose + ['exec', '-T', 'mock', 'python3', '-c', code], cwd=deploy, capture_output=True, text=True)
    if result.returncode:
        return None
    return json.loads(result.stdout)

def wait(fn, description):
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        value = fn()
        if value:
            return value
        time.sleep(3)
    raise AssertionError(description + ' not verified')

wait(lambda: (get('http://prometheus:9090/api/v1/query?' + urlencode({'query': 'up{job="live-platform"}'})) or {}).get('data', {}).get('result'), 'Prometheus scrape')
logs = wait(lambda: (get('http://loki:3100/loki/api/v1/query_range?' + urlencode({'query': '{service="live-platform"} |= "api_request"', 'limit': 100})) or {}).get('data', {}).get('result'), 'Alloy/Loki log retrieval')
assert any('trace_id' in item[1] and 'request_id' in item[1] for stream in logs for item in stream['values'])
traces = wait(lambda: (get('http://tempo:3200/api/search?' + urlencode({'tags': 'service.name=live-platform-api'})) or {}).get('traces'), 'Tempo API trace storage')
trace = get('http://tempo:3200/api/traces/' + traces[0]['traceID'])
assert trace, 'Stored trace could not be fetched'
chain = wait(lambda: (get('http://tempo:3200/api/search?' + urlencode({'tags': 'service.name=live-platform-sender'})) or {}).get('traces'), 'Sender trace storage')
# Inspect actual trace contents; a span in isolation is insufficient propagation proof.
propagated = False
for item in chain:
    detail = json.dumps(get('http://tempo:3200/api/traces/' + item['traceID']))
    if 'detector.probe' in detail and 'router.consume' in detail and 'sender.deliver' in detail:
        propagated = True
        break
assert propagated, 'Detector -> Kafka consumer -> durable task -> sender trace chain absent'

# An explicit disposable test alert exercises Prometheus-independent Alertmanager delivery.
code = '''import json
import http.cookiejar
from html.parser import HTMLParser
import ssl
from urllib.error import HTTPError
from urllib.request import build_opener, HTTPCookieProcessor, HTTPSHandler, ProxyHandler, Request,time,urllib.request
payload=[{"labels":{"alertname":"IntegrationDeliveryProof","severity":"test"},"annotations":{"summary":"disposable acceptance"}}]
r=urllib.request.Request("http://alertmanager:9093/api/v2/alerts",data=json.dumps(payload).encode(),headers={"Content-Type":"application/json"},method="POST")
with urllib.request.urlopen(r,timeout=10) as response: response.read()
'''
subprocess.run(compose + ['exec', '-T', 'mock', 'python3', '-c', code], cwd=deploy, check=True)
code = 'import urllib.request,ssl; print(urllib.request.urlopen("https://mock/test/state",context=ssl._create_unverified_context(),timeout=10).read().decode())'
def delivered():
    result = subprocess.run(compose + ['exec', '-T', 'mock', 'python3', '-c', code], cwd=deploy, capture_output=True, text=True, check=True)
    alerts = json.loads(result.stdout)['alerts']
    return any(a['labels']['alertname'] == 'IntegrationDeliveryProof' for group in alerts for a in group.get('alerts', []))
wait(delivered, 'Alertmanager webhook receipt')
# Log in through actual Grafana -> Keycloak -> callback using two distinct cookie jars.
class LoginForm(HTMLParser):
    action = None
    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == 'form' and values.get('id') == 'kc-form-login':
            self.action = values.get('action')

users = json.loads((deploy / 'generated/ci-login-users.json').read_text())
context = ssl.create_default_context(cafile=str(deploy / 'generated/certs/ca.crt'))
for username, password in users.items():
    session = build_opener(ProxyHandler({}), HTTPSHandler(context=context), HTTPCookieProcessor(http.cookiejar.CookieJar()))
    with session.open('https://grafana.localhost/login/generic_oauth', timeout=15) as response:
        form = LoginForm()
        form.feed(response.read().decode())
    assert form.action, 'Keycloak login form missing'
    data = urlencode({'username': username, 'password': password, 'credentialId': ''}).encode()
    try:
        with session.open(Request(form.action, data=data), timeout=15) as response:
            response.read()
    except HTTPError as error:
        error.read()
    try:
        with session.open('https://grafana.localhost/api/user', timeout=15) as response:
            authorized = response.status == 200
            response.read()
    except HTTPError as error:
        error.read()
        authorized = False
    assert authorized == (username == 'ci-operations'), 'Grafana operations-group isolation failed'
    if authorized:
        with session.open('https://grafana.localhost/api/dashboards/uid/live-platform', timeout=15) as response:
            dashboard = json.loads(response.read())
        assert dashboard['dashboard']['uid'] == 'live-platform' and len(dashboard['dashboard']['panels']) >= 5
print('Actual Loki search, cross-process trace chain, Prometheus scrape, alert receipt and operations-only Grafana dashboard verified')
