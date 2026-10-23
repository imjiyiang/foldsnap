#!/usr/bin/env python3
"""Collect notices from the resolved dependencies, without embedding local paths."""
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
raw = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=root, text=True)
decoder = json.JSONDecoder()
notices = ['# Third-party notices\n\nFoldsnap is MIT licensed. Dependencies retain their own licenses.\nThe following notices cover the resolved Go module graph (including build dependencies), Go runtime, and frontend runtime dependencies.\nRegenerate with `python3 scripts/collect-licenses.py` after installing Go/npm dependencies.\n']

def add(name, files):
    if not files:
        raise SystemExit('No license found for ' + name)
    notices.append('\n## ' + name + '\n')
    for file in files:
        notices.append('\n### ' + ('LICENSE' if name == 'Go runtime' else file.name) + '\n\n```text\n' + file.read_text().strip() + '\n```\n')

goroot = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
go_license = goroot / 'LICENSE'
if not go_license.exists():
    go_license = root / 'licenses/go-LICENSE'
add('Go runtime', [go_license])
pos = 0
while pos < len(raw):
    while pos < len(raw) and raw[pos].isspace():
        pos += 1
    if pos == len(raw):
        break
    module, pos = decoder.raw_decode(raw, pos)
    if module.get('Main'):
        continue
    directory_path = module.get('Dir')
    if not directory_path:
        downloaded = json.loads(subprocess.check_output(['go', 'mod', 'download', '-json', module['Path'] + '@' + module['Version']], cwd=root, text=True))
        directory_path = downloaded['Dir']
    directory = Path(directory_path)
    files = sorted(p for p in directory.iterdir() if p.is_file() and p.name.lower().startswith(('license', 'licence', 'copying')))
    add(module['Path'] + ' ' + module['Version'], files)
for name in ['react', 'react-dom', 'scheduler', 'lucide-react']:
    directory = root / 'apps/web/node_modules' / name
    package = json.loads((directory / 'package.json').read_text())
    files = sorted(p for p in directory.iterdir() if p.is_file() and p.name.lower().startswith(('license', 'licence', 'copying')))
    add(name + ' ' + package['version'], files)
output = ''.join(notices)
target = root / 'THIRD_PARTY_NOTICES.md'
if '--check' in sys.argv:
    if not target.exists() or target.read_text() != output:
        raise SystemExit('Third-party notices are stale; run python3 scripts/collect-licenses.py')
else:
    target.write_text(output)
