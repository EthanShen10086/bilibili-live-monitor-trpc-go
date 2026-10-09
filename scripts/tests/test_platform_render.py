import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]

class PlatformRenderTests(unittest.TestCase):
    def test_existing_directory_is_private_and_workers_can_read_config(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = ROOT / 'deploy/event-platform'
            shutil.copy(source / 'render.py', root)
            shutil.copytree(source / 'gateway', root / 'gateway')
            certs = root / 'certs'
            certs.mkdir()
            (certs / 'tls.crt').write_text('disposable certificate placeholder')
            (certs / 'tls.key').write_text('disposable key placeholder')
            output = root / 'generated'
            output.mkdir(mode=0o755)
            env = dict(os.environ, GRAFANA_OIDC_SECRET='test-only', API_HOST='api.localhost', AUTH_HOST='auth.localhost', GRAFANA_HOST='grafana.localhost')
            subprocess.run(['python3', str(root / 'render.py'), '--cert-dir', str(certs)], env=env, check=True, capture_output=True)
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            for name in ('apisix.yaml', 'realm.json', 'nginx.conf'):
                self.assertEqual((output / name).stat().st_mode & 0o777, 0o644)
            raw = (output / 'apisix.yaml').read_text()
            config = json.loads(raw.removesuffix('#END\n'))
            self.assertEqual(config['routes'][0]['plugins']['forward-auth']['request_headers'], ['Authorization'])
            self.assertEqual(config['routes'][0]['upstream']['retries'], 0)
            self.assertFalse(config['routes'][0]['plugins']['forward-auth']['allow_degradation'])
            self.assertEqual(config['routes'][0]['plugins']['forward-auth']['status_on_error'], 503)
            self.assertTrue(raw.endswith('#END\n'))
