#!/usr/bin/env python3
"""Render reviewed gateway/realm configuration. Never starts business workers."""
import argparse
import json
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cert-dir', type=Path, required=True)
    args = parser.parse_args()
    hosts = {key: os.environ.get(key, default) for key, default in (
        ('API_HOST', 'api.localhost'), ('AUTH_HOST', 'auth.localhost'), ('GRAFANA_HOST', 'grafana.localhost'))}
    for host in hosts.values():
        if not re.fullmatch(r'[a-zA-Z0-9.-]+', host):
            raise SystemExit('Hosts must be DNS names without ports or paths; TLS entry uses port 443')
    out = ROOT / 'generated'
    out.mkdir(mode=0o700, exist_ok=True)
    out.chmod(0o700)
    cert = args.cert_dir.joinpath('tls.crt').read_text()
    key = args.cert_dir.joinpath('tls.key').read_text()
    policy = json.loads((ROOT / 'gateway/policy.json').read_text())
    def upstream(address):
        return {'type': 'roundrobin', 'nodes': {address: 1}, 'retries': 0,
                'timeout': {'connect': 3, 'send': 12, 'read': 12}}
    api_plugins = {
        'forward-auth': {'uri': policy['auth_endpoint'], 'request_method': 'GET',
                         'request_headers': ['Authorization'], 'timeout': 3000},
        'limit-req': {'rate': policy['requests_per_second'], 'burst': policy['burst'],
                      'key': 'remote_addr', 'rejected_code': 429, 'nodelay': True},
        'request-id': {'header_name': 'X-Gateway-Request-ID', 'include_in_response': True},
        'prometheus': {},
        'proxy-rewrite': {'headers': {'remove': policy['strip_headers'],
                         'set': {'X-Forwarded-Proto': 'https', 'X-Forwarded-Host': '$host', 'X-Forwarded-For': '$remote_addr'}}},
        'response-rewrite': {'headers': {'set': {'Cache-Control': 'no-store'}}},
    }
    routes = [{'id': 'management', 'host': hosts['API_HOST'], 'uri': '/api/v1/*',
               'plugins': api_plugins, 'upstream': upstream('api:8800')}]
    for route_id, uri in (('oidc', '/realms/live/*'), ('oidc-resources', '/resources/*')):
        routes.append({'id': route_id, 'host': hosts['AUTH_HOST'], 'uri': uri,
                       'plugins': {'proxy-rewrite': {'headers': {'set': {'X-Forwarded-Proto': 'https', 'X-Forwarded-Host': '$host', 'X-Forwarded-For': '$remote_addr'}}}},
                       'upstream': upstream('keycloak:8080')})
    routes.append({'id': 'operations', 'host': hosts['GRAFANA_HOST'], 'uri': '/*',
                   'upstream': upstream('grafana:3000'),
                   'plugins': {'proxy-rewrite': {'headers': {'set': {'X-Forwarded-Proto': 'https', 'X-Forwarded-Host': '$host'}}}}})
    config = {'routes': routes, 'ssls': [{'id': 'entry', 'snis': list(hosts.values()), 'cert': cert, 'key': key}]}
    # JSON is valid YAML; standalone file mode requires the final END marker.
    (out / 'apisix.yaml').write_text(json.dumps(config, indent=2) + '\n#END\n')
    nginx = (ROOT / 'gateway/nginx.conf.template').read_text()
    for name, host in hosts.items():
        nginx = nginx.replace('@' + name + '@', host)
    (out / 'nginx.conf').write_text(nginx)
    secret = os.environ.get('GRAFANA_OIDC_SECRET')
    if not secret:
        raise SystemExit('GRAFANA_OIDC_SECRET required')
    realm = {'realm': 'live', 'enabled': True, 'sslRequired': 'external',
             'registrationAllowed': False, 'groups': [{'name': 'operations'}],
             'clients': [
                 {'clientId': 'live-api', 'enabled': True, 'publicClient': True,
                  'standardFlowEnabled': True, 'directAccessGrantsEnabled': False,
                  'redirectUris': [f"https://{hosts['API_HOST']}/oidc/callback"],
                  'protocolMappers': [{'name': 'api-audience', 'protocol': 'openid-connect',
                                      'protocolMapper': 'oidc-audience-mapper',
                                      'config': {'included.client.audience': 'live-api', 'access.token.claim': 'true'}}]},
                 {'clientId': 'grafana', 'enabled': True, 'secret': secret,
                  'publicClient': False, 'standardFlowEnabled': True,
                  'redirectUris': [f"https://{hosts['GRAFANA_HOST']}/login/generic_oauth"],
                  'protocolMappers': [{'name': 'groups', 'protocol': 'openid-connect',
                                      'protocolMapper': 'oidc-group-membership-mapper',
                                      'config': {'claim.name': 'groups', 'full.path': 'false',
                                                 'id.token.claim': 'true', 'access.token.claim': 'true', 'userinfo.token.claim': 'true'}}]}
             ]}
    (out / 'realm.json').write_text(json.dumps(realm, indent=2))
    for path in out.iterdir():
        if path.is_file():
            path.chmod(0o644 if path.name in ("realm.json", "nginx.conf", "apisix.yaml") else 0o600)
    print('Rendered gateway and Keycloak files; no services started.')

if __name__ == '__main__':
    main()
