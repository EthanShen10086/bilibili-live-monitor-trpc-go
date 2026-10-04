#!/usr/bin/env python3
"""Check Git's actual upload manifest, without printing file contents."""
import pathlib, re, subprocess, sys
root = pathlib.Path(__file__).resolve().parents[1]
files = subprocess.check_output(['git', 'ls-files', '-z'], cwd=root).decode().split('\0')
errors = []
for name in filter(None, files):
    p = pathlib.PurePosixPath(name)
    if any(x in p.parts for x in ('node_modules', '.cache', '.runtime', '.npm-cache', '.node-gyp', 'dist', 'var', 'evidence')) or (p.name.startswith('.env') and p.name != '.env.example') or p.suffix in ('.sqlite', '.db', '.log', '.test', '.out'):
        errors.append(name + ': excluded runtime file')
        continue
    data = (root / name).read_bytes()
    if re.search(rb'https://open\.feishu\.cn/open-apis/bot/v2/hook/[a-zA-Z0-9-]{10,}', data) or re.search(rb'\b(?:ghp_|gho_|github_pat_)[A-Za-z0-9_]{20,}', data) or re.search(rb'(?m)^-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----$', data):
        errors.append(name + ': credential pattern')
if errors:
    print('\n'.join(errors)); sys.exit(1)
print('Public file manifest checked:', len(list(filter(None, files))), 'files; no runtime files or credential patterns.')
