#!/usr/bin/env python3
"""Git quality hooks check immutable snapshots and preserve existing global hooks."""
import io
import os
import pathlib
import re
import subprocess
import sys
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
MARKER = '# bilibili-monitor quality dispatcher'
NAMES = ('pre-commit', 'commit-msg', 'pre-push')

def git(*args, cwd=None):
    cwd = ROOT if cwd is None else cwd
    return subprocess.check_output(['git', *args], cwd=cwd).decode().strip()

def validate_message(message):
    subject = next((line for line in message.splitlines() if line and not line.startswith('#')), '')
    if subject.startswith(('Merge ', 'Revert "')):
        return
    pattern = r'^(feat|fix|refactor|test|docs|style|perf|build|ci|revert|chore)(\([a-z0-9_./-]+\))?!?: \S.*$'
    if len(subject) > 100 or not re.match(pattern, subject):
        raise RuntimeError('commit subject must be type(scope): description, <=100 characters; see CONTRIBUTING.md')

def export_commit(revision, destination):
    if not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', revision):
        raise RuntimeError('invalid commit object ID')
    archive = subprocess.check_output(['git', 'archive', '--format=tar', revision], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        for member in source:
            path = pathlib.PurePosixPath(member.name)
            if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
                raise RuntimeError('unsupported path in source snapshot')
            target = destination / member.name
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(source.extractfile(member).read())
                target.chmod(member.mode)

def check_snapshot(action, revision=None):
    with tempfile.TemporaryDirectory(prefix='bili-quality-') as directory:
        snapshot = pathlib.Path(directory)
        if revision:
            export_commit(revision, snapshot)
        else:
            # Export the index, not the working tree. Never stage or format for the user.
            subprocess.run(['git', 'checkout-index', '--all', '--prefix=' + directory + os.sep], cwd=ROOT, check=True)
        runner = snapshot / 'scripts/quality.py'
        if not runner.exists():
            raise RuntimeError('snapshot lacks scripts/quality.py; merge the quality setup before pushing')
        env = dict(os.environ, MONITOR_QUALITY_TOOLS=str(ROOT / '.cache/tools'))
        # Git exports repository-local variables while invoking hooks. They must
        # not leak into tests that create their own disposable repositories.
        for variable in git('rev-parse', '--local-env-vars').splitlines():
            env.pop(variable, None)
        if action == 'pre-commit':
            actions = ('fmt-check', 'lint', 'test')
        else:
            actions = ('verify',)
        for check in actions:
            subprocess.run([sys.executable, str(runner), check], cwd=snapshot, env=env, check=True)
        if action == 'pre-commit':
            subprocess.run([sys.executable, str(snapshot / 'scripts/check-public-files.py'), '--snapshot'], cwd=snapshot, env=env, check=True)

def install():
    common = pathlib.Path(git('rev-parse', '--git-common-dir'))
    if not common.is_absolute():
        common = ROOT / common
    hooks = common / 'hooks'
    result = subprocess.run(['git', 'config', '--path', '--get', 'core.hooksPath'], cwd=ROOT, text=True, capture_output=True)
    if result.returncode == 0:
        configured = pathlib.Path(result.stdout.strip())
        if not configured.is_absolute():
            configured = ROOT / configured
        # The existing corporate dispatchers explicitly delegate to .git/hooks.
        # Never override core.hooksPath or any security/push approval hook.
        if configured.resolve() != hooks.resolve():
            for name in NAMES:
                dispatcher = configured / name
                if not dispatcher.is_file() or f'.git/hooks/{name}' not in dispatcher.read_text():
                    raise RuntimeError(f'custom {name} does not delegate to repository hooks; add an explicit approved chain manually')
            if name == 'pre-push' and (configured / 'other-pre-push').exists():
                raise RuntimeError('other-pre-push takes precedence; wire project checks into that approved chain manually')
    hooks.mkdir(parents=True, exist_ok=True)
    # Validate all destinations before writing any of them.
    for name in NAMES:
        target = hooks / name
        if target.exists() and MARKER not in target.read_text():
            raise RuntimeError(f'existing {name} preserved; review and chain hooks manually')
    for name in NAMES:
        target = hooks / name
        target.write_text(f'''#!/bin/sh
{MARKER}
set -eu
root=$(git rev-parse --show-toplevel)
# Shared Git metadata may include older worktrees without this feature yet.
[ -f "$root/scripts/hooks.py" ] || exit 0
exec python3 "$root/scripts/hooks.py" {name} "$@"
''')
        target.chmod(0o755)
    print('Installed repository dispatchers; core.hooksPath and global hooks unchanged.')

def main(args):
    if not args:
        raise RuntimeError('usage: hooks.py install|pre-commit|commit-msg|pre-push')
    action = args[0]
    if action == 'install':
        install()
    elif action == 'commit-msg':
        validate_message(pathlib.Path(args[1]).read_text())
    elif action == 'pre-commit':
        check_snapshot(action)
    elif action == 'pre-push':
        revisions = set()
        for line in sys.stdin:
            parts = line.split()
            if len(parts) != 4:
                raise RuntimeError('invalid pre-push input')
            revision = parts[1]
            if set(revision) != {'0'}:
                revisions.add(revision)
        for revision in sorted(revisions):
            check_snapshot(action, revision)
    else:
        raise RuntimeError('unknown hook')

if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except (RuntimeError, subprocess.CalledProcessError, OSError) as error:
        print(f'quality hook: {error}', file=sys.stderr)
        sys.exit(1)
