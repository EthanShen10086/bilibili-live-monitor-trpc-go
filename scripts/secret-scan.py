#!/usr/bin/env python3
"""Scan publishable files/history; always redact scanner output, including failures."""
import argparse
import json
import pathlib
import shutil
import subprocess
import tempfile
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]

def checked_scan(command, cwd):
    with tempfile.TemporaryDirectory(prefix='bili-secret-report-') as temporary:
        report = pathlib.Path(temporary) / 'redacted.json'
        result = subprocess.run([*command, '--report-format', 'json', '--report-path', str(report)], cwd=cwd)
        if result.returncode:
            if report.exists():
                for finding in json.loads(report.read_text()):
                    print(f"{finding['File']}:{finding['StartLine']}: {finding['RuleID']}", file=sys.stderr)
            raise subprocess.CalledProcessError(result.returncode, command)

def scan(binary, history=False):
    common = ['--config', str(ROOT / '.gitleaks.toml'), '--redact=100', '--no-banner', '--log-level', 'error']
    if history:
        checked_scan([binary, 'git', '--log-opts=--all', *common], ROOT)
        print('Secret scan passed: all locally available Git refs/history; output redacted.')
        return
    if (ROOT / '.git').exists():
        names = subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0')
    else:
        names = [str(p.relative_to(ROOT)) for p in ROOT.rglob('*') if p.is_file() and '.cache' not in p.parts]
    with tempfile.TemporaryDirectory(prefix='bili-secrets-') as temporary:
        destination = pathlib.Path(temporary)
        for name in filter(None, names):
            source = ROOT / name
            if not source.is_file():
                continue
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
        checked_scan([binary, 'dir', '.', *common], destination)
    print('Secret scan passed: publishable source snapshot; output redacted.')

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary')
    parser.add_argument('--history', action='store_true')
    args = parser.parse_args()
    scan(str(pathlib.Path(args.binary).resolve()), args.history)
