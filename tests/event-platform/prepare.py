#!/usr/bin/env python3
"""Generate disposable CI secrets and certificates. No public services are contacted."""
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess

root = Path(__file__).resolve().parents[2]
deploy = root / 'deploy/event-platform'
generated = deploy / 'generated'
certs = generated / 'certs'
certs.mkdir(parents=True, mode=0o700, exist_ok=True)
generated.chmod(0o700)
subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                '-keyout', str(certs / 'tls.key'), '-out', str(certs / 'tls.crt'), '-subj', '/CN=api.localhost',
                '-addext', 'subjectAltName=DNS:api.localhost,DNS:auth.localhost,DNS:grafana.localhost,DNS:mailpit,DNS:open.feishu.cn,DNS:api.live.bilibili.com,IP:127.0.0.1',
                '-addext', 'basicConstraints=critical,CA:TRUE'], check=True, capture_output=True)
(certs / 'ca.crt').write_bytes((certs / 'tls.crt').read_bytes())
for file in certs.iterdir():
    file.chmod(0o644 if file.suffix == '.crt' else 0o600)
env = {'TLS_DIR': str(certs), 'PG_PASSWORD': secrets.token_hex(16), 'KC_PG_PASSWORD': secrets.token_hex(16),
       'KC_ADMIN_PASSWORD': secrets.token_hex(16), 'GRAFANA_OIDC_SECRET': secrets.token_hex(16),
       'EVENT_CREDENTIAL_KEY_ID': 'ci', 'EVENT_CREDENTIAL_KEYS': json.dumps({'ci': base64.b64encode(secrets.token_bytes(32)).decode()}),
       'EVENT_ADMIN_SUBJECTS': '00000000-0000-4000-8000-000000000001',
       'EVENT_SMTP_ALLOWED_HOSTS': 'mailpit'}
(deploy / '.env').write_text(''.join(f"{key}='{value}'\n" for key, value in env.items()))
(deploy / '.env').chmod(0o600)
subprocess.run(['python3', str(deploy / 'render.py'), '--cert-dir', str(certs)], env={**os.environ, **env}, check=True)
realm = json.loads((generated / 'realm.json').read_text())
clients = {}
for i, name in enumerate(('integration', 'outsider', 'viewer'), 1):
    secret = secrets.token_hex(16)
    clients[name] = secret
    realm['clients'].append({'clientId': name, 'secret': secret, 'enabled': True, 'publicClient': False,
                             'serviceAccountsEnabled': True, 'standardFlowEnabled': False,
                             'protocolMappers': [{'name': 'audience', 'protocol': 'openid-connect', 'protocolMapper': 'oidc-audience-mapper',
                                                 'config': {'included.client.audience': 'live-api', 'access.token.claim': 'true'}}]})
    realm.setdefault('users', []).append({'id': f'00000000-0000-4000-8000-{i:012d}', 'username': f'service-account-{name}',
                                          'enabled': True, 'serviceAccountClientId': name})
login_users = {}
for i, username in enumerate(('ci-operations', 'ci-non-operations'), 4):
    password = secrets.token_hex(16)
    login_users[username] = password
    realm.setdefault('users', []).append({'id': f'00000000-0000-4000-8000-{i:012d}', 'username': username,
        'enabled': True, 'email': username + '@example.test', 'emailVerified': True,
        'firstName': 'Integration', 'lastName': 'Operator', 'groups': ['operations'] if i == 4 else [],
        'credentials': [{'type': 'password', 'value': password, 'temporary': False}]})
(generated / 'ci-login-users.json').write_text(json.dumps(login_users))
(generated / 'ci-login-users.json').chmod(0o600)
(generated / 'realm.json').write_text(json.dumps(realm))
(generated / 'ci-clients.json').write_text(json.dumps(clients))
(generated / 'ci-clients.json').chmod(0o600)
(generated / 'alertmanager-test.yaml').write_text('''global:
  resolve_timeout: 1m
route:
  receiver: test
  group_wait: 1s
  group_interval: 5s
  repeat_interval: 1m
receivers:
  - name: test
    webhook_configs:
      - url: https://mock/test/alerts
        http_config:
          tls_config:
            insecure_skip_verify: true
''')
print('Disposable CI configuration prepared; secret values withheld.')
