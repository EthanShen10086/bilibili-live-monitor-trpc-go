"""Regression tests for staged/committed source selection and message policy."""
import importlib.util
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

GIT_ENV = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / 'hooks.py'
spec = importlib.util.spec_from_file_location('hooks', SCRIPT)
hooks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hooks)

class HookTests(unittest.TestCase):
    def test_message_policy(self):
        for message in ('feat(queue): preserve durable jobs', 'fix!: change a contract', 'Merge branch feature', 'Revert "feat: example"'):
            hooks.validate_message(message)
        for message in ('WIP', 'fixed stuff', 'feat: ', 'feat: ' + 'x' * 101):
            with self.assertRaises(RuntimeError):
                hooks.validate_message(message)

    def test_index_snapshot_preserves_partial_staging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            def git(*args):
                return subprocess.check_output(['git', '-c', 'core.hooksPath=/dev/null', *args], cwd=root, env=GIT_ENV)
            git('init', '-q')
            (root / 'scripts').mkdir()
            (root / 'scripts/quality.py').write_text('# staged runner')
            (root / 'scripts/check-public-files.py').write_text('# staged manifest')
            (root / 'data.txt').write_text('staged good')
            git('add', '.')
            before = git('write-tree')
            (root / 'data.txt').write_text('unstaged bad')
            real_run = subprocess.run
            observed = []
            def run(args, **kwargs):
                if args[0] == 'git':
                    return real_run(args, **kwargs)
                observed.append((pathlib.Path(kwargs['cwd']) / 'data.txt').read_text())
                self.assertNotIn('GIT_INDEX_FILE', kwargs['env'])
                self.assertNotIn('GIT_DIR', kwargs['env'])
                return subprocess.CompletedProcess(args, 0)
            with mock.patch.object(hooks, 'ROOT', root), mock.patch.dict(os.environ, {'GIT_INDEX_FILE': str(root / '.git/index')}), mock.patch.object(hooks.subprocess, 'run', side_effect=run):
                hooks.check_snapshot('pre-commit')
            self.assertEqual(observed, ['staged good'] * 5)
            self.assertEqual(before, git('write-tree'))
            self.assertEqual((root / 'data.txt').read_text(), 'unstaged bad')

    def test_commit_export_ignores_dirty_working_tree(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as output:
            root = pathlib.Path(directory)
            def git(*args):
                return subprocess.check_output(['git', '-c', 'core.hooksPath=/dev/null', *args], cwd=root, env=GIT_ENV)
            git('init', '-q')
            git('config', 'user.name', 'Hook Test')
            git('config', 'user.email', 'hook-test@example.invalid')
            (root / 'data.txt').write_text('committed good')
            git('add', '.')
            git('commit', '-qm', 'test: snapshot')
            revision = git('rev-parse', 'HEAD').decode().strip()
            (root / 'data.txt').write_text('dirty bad')
            with mock.patch.object(hooks, 'ROOT', root):
                hooks.export_commit(revision, pathlib.Path(output))
            self.assertEqual((pathlib.Path(output) / 'data.txt').read_text(), 'committed good')

    def test_install_preserves_existing_hooks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subprocess.run(['git', 'init', '-q', directory], check=True, env=GIT_ENV)
            subprocess.run(['git', 'config', '--local', 'core.hooksPath', '.git/hooks'], cwd=root, check=True, env=GIT_ENV)
            target = root / '.git/hooks/pre-commit'
            target.write_text('# existing company hook')
            with mock.patch.object(hooks, 'ROOT', root), mock.patch.object(hooks, 'git', return_value=str(root / '.git')):
                with self.assertRaises(RuntimeError):
                    hooks.install()
            self.assertEqual(target.read_text(), '# existing company hook')
            self.assertFalse((root / '.git/hooks/pre-push').exists())

if __name__ == '__main__':
    unittest.main()
