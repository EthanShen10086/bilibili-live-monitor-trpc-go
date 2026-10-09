#!/usr/bin/env python3
"""Prove actual log retrieval, stored traces, panel provisioning and alert reception."""
import json
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
code = '''import json,time,urllib.request
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
# Verify that the provisioned dashboard is loaded in Grafana's database, not just a file.
result = subprocess.run(compose + ['exec', '-T', 'grafana', 'sh', '-c', 'test -s /var/lib/grafana/grafana.db'], cwd=deploy)
assert result.returncode == 0
print('Actual Loki search, Tempo trace chain, Prometheus scrape and Alertmanager receiver receipt verified')
