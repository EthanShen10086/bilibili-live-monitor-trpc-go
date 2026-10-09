#!/usr/bin/env python3
"""Run the same ingress and event-path contracts through either real gateway."""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import ssl
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import HTTPSHandler, ProxyHandler, Request, build_opener

root = Path(__file__).resolve().parents[2]
deploy = root / 'deploy/event-platform'
context = ssl.create_default_context(cafile=str(deploy / 'generated/certs/ca.crt'))
opener = build_opener(ProxyHandler({}), HTTPSHandler(context=context))
gateway = sys.argv[1]
compose = ['docker', 'compose', '-f', 'compose.yaml', '-f', '../../tests/event-platform/compose.yaml']
def service(*args):
    return subprocess.run(compose + list(args), cwd=deploy, check=True, capture_output=True, text=True).stdout

clients = json.loads((deploy / 'generated/ci-clients.json').read_text())
def request(path, method='GET', body=None, token=None, host='api.localhost', headers=None, form=False, pace=True):
    if pace:
        time.sleep(.14)
    url = path if path.startswith('http') else f'https://{host}{path}'
    data = (urlencode(body).encode() if form else json.dumps(body).encode()) if body is not None else None
    all_headers = {'Content-Type': 'application/x-www-form-urlencoded' if form else 'application/json'}
    if token:
        all_headers['Authorization'] = 'Bearer ' + token
    all_headers.update(headers or {})
    try:
        with opener.open(Request(url, data=data, method=method, headers=all_headers), timeout=15) as response:
            payload = response.read()
            return response.status, json.loads(payload) if payload else None
    except HTTPError as error:
        payload = error.read()
        try:
            value = json.loads(payload)
        except ValueError:
            value = None
        return error.code, value

def wait(fn, description, timeout=180):
    end = time.monotonic() + timeout
    last = None
    while time.monotonic() < end:
        try:
            result = fn()
            if result:
                return result
        except (URLError, OSError, ValueError) as error:
            last = type(error).__name__
        time.sleep(2)
    raise AssertionError(f'{description} timed out ({last})')

def token_for(name):
    status, body = request('/realms/live/protocol/openid-connect/token', 'POST',
                           {'grant_type': 'client_credentials', 'client_id': name, 'client_secret': clients[name]}, host='auth.localhost', form=True)
    return body['access_token'] if status == 200 else None

admin = wait(lambda: token_for('integration'), 'real Keycloak token')
outsider = wait(lambda: token_for('outsider'), 'outsider token')
viewer = wait(lambda: token_for('viewer'), 'viewer token')
status, device = request('/realms/live/protocol/openid-connect/auth/device', 'POST',
                         {'client_id': 'live-api', 'scope': 'openid'}, host='auth.localhost', form=True)
assert status == 200 and all(key in device for key in ('device_code', 'user_code', 'verification_uri', 'interval')), 'CLI device authorization unavailable'
wait(lambda: request('/api/v1/tenants', token=admin)[0] == 200, 'verified API')
for path in ('/api/v1/tenants', '/api/v1/tenants?unexpected=1'):
    assert request(path)[0] == 401
    assert request(path, token='not.a.jwt')[0] == 401
for path in ('/metrics', '/internal/auth', '/readyz', '/livez', '/_authenticate', '/admin'):
    assert request(path, token=admin)[0] == 404, (gateway, path)
assert request('/admin/', host='auth.localhost')[0] == 404
assert request('/api/v1/tenants', 'POST', {'name': 'spoof'}, token=outsider,
               headers={'X-User': '00000000-0000-4000-8000-000000000001', 'X-Roles': 'owner', 'X-Tenant-ID': 'legacy-default'})[0] == 403
status, tenant = request('/api/v1/tenants', 'POST', {'name': gateway, 'max_subscriptions': 2, 'max_targets': 3}, admin)
assert status == 200, ('tenant creation', status)
base = '/api/v1/tenants/' + tenant['id']
assert request(base + '/subscriptions', token=outsider)[0] == 403
assert request(base + '/members/00000000-0000-4000-8000-000000000003', 'PUT', {'role': 'viewer'}, admin)[0] == 200
assert request(base + '/members', token=viewer)[0] == 200
assert request(base + '/targets', 'POST', {'name': 'forbidden'}, viewer)[0] in (400, 403)

# Independent channels share one detected room. One temporary Feishu failure cannot suppress SMTP.
service('stop', '--timeout', '10', 'kafka')
request('https://127.0.0.1:19443/test/state', 'POST', {'live': 1, 'start': int(time.time()), 'fail_group': 1})
targets = []
for kind, credentials in (
    ('feishu_group', {'webhook': 'https://open.feishu.cn/open-apis/bot/v2/hook/test', 'secret': 'test-only-secret'}),
    ('feishu_private', {'app_id': 'mock', 'secret': 'test-only-secret', 'recipient': 'test-user', 'id_type': 'open_id'}),
    ('smtp', {'smtp_host': 'mailpit', 'smtp_port': 1025, 'from': 'monitor@example.test', 'recipient': 'user@example.test'})):
    status, target = request(base + '/targets', 'POST', {'name': kind, 'kind': kind, 'credentials': credentials}, admin)
    assert status == 200, ('target', kind, status)
    assert 'credentials' not in target and 'secret' not in json.dumps(target)
    targets.append(target['id'])
policy = {'timezone': 'Asia/Shanghai', 'weekdays': [1, 2, 3, 4, 5, 6, 7], 'start': '00:00', 'end': '24:00',
          'interval_seconds': 10, 'notified_seconds': 30, 'ttl_minutes': 30}
room = 1616 if gateway == 'apisix' else 2626
status, sub = request(base + '/subscriptions', 'POST', {'room_id': room, 'enabled': True, 'policy': policy, 'target_ids': targets}, admin)
assert status == 200, ('subscription', status)
status, sub2 = request(base + '/subscriptions', 'POST', {'room_id': room, 'enabled': True, 'policy': policy, 'target_ids': [targets[-1]]}, admin)
assert status == 200
assert request(base + '/subscriptions', 'POST', {'room_id': room + 1, 'enabled': True, 'policy': policy, 'target_ids': targets}, admin)[0] == 429
update = {key: sub[key] for key in ('room_id', 'enabled', 'policy', 'target_ids', 'version')}
assert request(base + '/subscriptions/' + sub['id'], 'PUT', {**update, 'version': 0}, admin)[0] == 409

def jobs_done():
    status, jobs = request(base + '/notifications', token=admin)
    return jobs if status == 200 and len(jobs) == 4 and all(job['state'] == 'sent' for job in jobs) else None
wait(lambda: int(service('exec','-T','postgres','psql','-U','platform','-d','platform','-Atc',f'SELECT count(*) FROM ep_events WHERE room={room} AND NOT published').strip()) > 0, 'durable Outbox during actual broker outage', 90)
assert request(base + '/notifications', token=admin)[1] == [], 'snapshot bypassed Kafka outage'
service('start','kafka')
jobs = wait(jobs_done, 'Kafka to four independent durable notifications', 240)
assert len({(j['event_id'], j['subscription_id'], j['target_id']) for j in jobs}) == 4
assert len({j['event_id'] for j in jobs}) == 1
assert max(j['attempts'] for j in jobs) >= 2, 'temporary provider failure did not retry'
mail = request('http://127.0.0.1:18025/api/v1/messages')[1]
assert mail['total'] >= 2, 'SMTP acceptance not visible'

# Replay only projects history; job IDs, states and external acceptance remain unchanged.
before = request('https://127.0.0.1:19443/test/state')[1]['accepted']
now = datetime.now(timezone.utc)
assert request(base + '/replays', 'POST', {'from': (now - timedelta(hours=1)).isoformat(), 'to': now.isoformat()}, admin)[0] == 200
wait(lambda: all(r['state'] == 'completed' for r in request(base + '/replays', token=admin)[1]), 'projection replay')
after = request(base + '/notifications', token=admin)[1]
assert {(j['id'], j['state']) for j in jobs} == {(j['id'], j['state']) for j in after}
assert before == request('https://127.0.0.1:19443/test/state')[1]['accepted']
assert request(base + '/statistics', token=viewer)[0] == 200
assert request(base + '/statistics', token=outsider)[0] == 403
assert request(base + '/credentials/rotate', 'POST', {}, admin)[0] == 200
assert request('/api/v1/tenants', 'POST', {'name': 'x' * 70000}, admin)[0] in (400, 413)
with ThreadPoolExecutor(max_workers=30) as pool:
    statuses = list(pool.map(lambda _: request('/api/v1/tenants', token=admin, pace=False)[0], range(80)))
assert 429 in statuses, 'gateway rate limit absent'
time.sleep(4)

# Management authentication failure must reject access while detection remains alive.
probes = request('https://127.0.0.1:19443/test/state')[1]['probes']
service('stop','--timeout','15','api')
assert request('/api/v1/tenants', token=admin)[0] in (500, 502, 503, 504)
wait(lambda: request('https://127.0.0.1:19443/test/state')[1]['probes'] > probes, 'detector progress during management API outage', 75)
service('start','api')
wait(lambda: request('/api/v1/tenants', token=admin)[0] == 200, 'API recovery')

# Stop subscriptions before switching gateways; keep recorded history readable.
for subscription in (sub, sub2):
    payload = {key: subscription[key] for key in ('room_id', 'enabled', 'policy', 'target_ids', 'version')}
    payload['enabled'] = False
    assert request(base + '/subscriptions/' + subscription['id'], 'PUT', payload, admin)[0] == 200
print(f'{gateway}: real OIDC, tenant isolation, limits, shared detection, Kafka routing, SMTP/Feishu acceptance and replay contracts passed')
