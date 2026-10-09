import pathlib
import shutil
import subprocess
import tempfile
import unittest


class CloudPrepareTests(unittest.TestCase):
    def test_storage_and_cache_are_independent(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        for storage, cache in (('sqlite', 'memory'), ('sqlite', 'redis'), ('postgres', 'redis')):
            with self.subTest(storage=storage, cache=cache), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                cloud = root / 'deploy' / 'cloud'
                cloud.mkdir(parents=True)
                for name in ('manage.sh', 'config.light.yaml', 'config.postgres.yaml', '.env.example'):
                    shutil.copyfile(repo / 'deploy' / 'cloud' / name, cloud / name)
                shutil.copyfile(repo / 'trpc_go.yaml', root / 'trpc_go.yaml')
                result = subprocess.run(['sh', str(cloud / 'manage.sh'), storage, 'prepare', cache], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                text = (cloud / 'config.yaml').read_text()
                self.assertIn('storage: ' + storage, text)
                self.assertIn('cache: ' + cache, text)
                self.assertIn('queue: database', text)
                self.assertEqual((cloud / '.env').stat().st_mode & 0o777, 0o600)
                if storage == 'postgres':
                    sender = (cloud / 'config.sender.yaml').read_text()
                    self.assertIn('cache: ' + cache, sender)
                    self.assertIn('role: sender', sender)
                again = subprocess.run(['sh', str(cloud / 'manage.sh'), storage, 'prepare', cache], capture_output=True)
                self.assertNotEqual(again.returncode, 0)
                self.assertEqual(text, (cloud / 'config.yaml').read_text())


if __name__ == '__main__':
    unittest.main()
