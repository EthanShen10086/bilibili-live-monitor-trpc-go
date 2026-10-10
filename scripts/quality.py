#!/usr/bin/env python3
"""One quality entry point for developers, Git hooks, and CI. Checks never edit files."""
import argparse
import ast
import json
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
TOOLS = pathlib.Path(os.environ.get('MONITOR_QUALITY_TOOLS', ROOT / '.cache/tools'))
VERSIONS = json.loads((ROOT / 'scripts/tool-versions.json').read_text())

def run(args, **kwargs):
    print('+ ' + ' '.join(map(str, args)), flush=True)
    subprocess.run(args, cwd=ROOT, check=True, **kwargs)

def tool(name):
    binary = TOOLS / name
    package, version = VERSIONS[name].rsplit('@', 1)
    module = package.split('/cmd/')[0]
    if not binary.is_file():
        raise RuntimeError(f'{name} missing: run make tools (fixed versions, explicit install)')
    metadata = subprocess.check_output(['go', 'version', '-m', str(binary)], text=True)
    if not any(line.split()[:3] == ['mod', module, version] for line in metadata.splitlines()):
        raise RuntimeError(f'{name} version mismatch: run make tools')
    return str(binary)

def install():
    TOOLS.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOBIN=str(TOOLS.resolve()))
    for package in VERSIONS.values():
        run(['go', 'install', package], env=env)

def formatting(write=False):
    for command in (
        [tool('goimports'), '-local', 'github.com/EthanShen10086/bilibili-live-monitor-trpc-go', '-w' if write else '-l', 'cmd', 'internal'],
        [tool('gofumpt'), '-w' if write else '-l', 'cmd', 'internal'],
    ):
        if write:
            run(command)
        else:
            result = subprocess.check_output(command, cwd=ROOT, text=True)
            if result.strip():
                raise RuntimeError('format/import differences; run make fmt:\n' + result)

def lint():
    run([tool('golangci-lint'), 'config', 'verify'])
    run([tool('golangci-lint'), 'run'])

def unit():
    for source in (ROOT / 'scripts').rglob('*.py'):
        ast.parse(source.read_text(), filename=str(source))
    for source in list((ROOT / 'bin').iterdir()) + list((ROOT / 'deploy').rglob('*.sh')):
        if source.is_file() and source.read_text().startswith('#!/'):
            run(['sh', '-n', str(source)])
    run(['python3', '-m', 'unittest', 'discover', '-s', 'scripts/tests', '-v'])
    run(['go', 'test', '-race', '-timeout=2m', './...'])

def integration():
    if not all(os.environ.get(name) for name in ('MONITOR_TEST_POSTGRES', 'MONITOR_TEST_REDIS')):
        raise RuntimeError('integration requires disposable MONITOR_TEST_POSTGRES and MONITOR_TEST_REDIS; never use production')
    run(['go', 'test', '-race', '-tags=integration', '-timeout=3m', '-coverprofile=coverage.out', './...'])

def manifest():
    args = ['python3', 'scripts/check-public-files.py']
    if not (ROOT / '.git').exists():
        args.append('--snapshot')
    run(args)

def verify():
    formatting()
    lint()
    unit()
    run(['go', 'mod', 'tidy', '-diff'])
    run([tool('govulncheck'), './...'])
    manifest()
    secrets()

def secrets(history=False):
    run(['python3', 'scripts/secret-scan.py', tool('gitleaks'), *(['--history'] if history else [])])

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['tools', 'fmt', 'fmt-check', 'lint', 'test', 'integration', 'verify', 'secrets', 'secrets-history'])
    action = parser.parse_args().action
    actions = {'tools': install, 'fmt': lambda: formatting(True), 'fmt-check': formatting, 'lint': lint, 'test': unit, 'integration': integration, 'verify': verify, 'secrets': secrets, 'secrets-history': lambda: secrets(True)}
    try:
        actions[action]()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(f'quality: {error}', file=sys.stderr)
        sys.exit(1)
