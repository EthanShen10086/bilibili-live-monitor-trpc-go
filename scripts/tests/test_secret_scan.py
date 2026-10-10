import os
import secrets
import string
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]

class SecretScanTests(unittest.TestCase):
    def test_precise_noncredential_exception_does_not_hide_tokens(self):
        binary = Path(os.environ.get('MONITOR_QUALITY_TOOLS', ROOT / '.cache/tools')) / 'gitleaks'
        self.assertTrue(binary.is_file(), 'make tools must install the fixed secret scanner')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / 'deploy/event-platform/compose.yaml'
            source.parent.mkdir(parents=True)
            source.write_text('KC_DB_URL: jdbc:postgresql://' + 'keycloak-db' + ':5432/' + 'keycloak\n')
            command = [str(binary.resolve()), 'dir', '.', '--config', str(ROOT / '.gitleaks.toml'),
                       '--redact=100', '--no-banner', '--log-level', 'error']
            clean = subprocess.run(command, cwd=root, capture_output=True, text=True)
            self.assertEqual(clean.returncode, 0)
            token = 'ghp' + '_' + ''.join(secrets.choice(string.ascii_letters + string.digits) for _ in range(36))
            source.write_text(source.read_text() + 'token: ' + token + '\n')
            finding = subprocess.run(command, cwd=root, capture_output=True, text=True)
            self.assertEqual(finding.returncode, 1)
            self.assertNotIn(token, finding.stdout + finding.stderr)
