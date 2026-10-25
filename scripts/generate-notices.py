#!/usr/bin/env python3
"""Regenerate notices for modules linked into the duplex binary."""
import json
import os
import subprocess
from pathlib import Path

go = os.environ.get('GO', 'go')
paths = sorted(set(subprocess.check_output([go, 'list', '-deps', '-f', '{{if .Module}}{{.Module.Path}}{{end}}', './cmd/duplex'], text=True).split()) - {'duplex'})
raw = subprocess.check_output([go, 'list', '-m', '-json', *paths], text=True)
modules = []
decoder = json.JSONDecoder()
while raw.strip():
    module, end = decoder.raw_decode(raw.lstrip())
    modules.append(module)
    raw = raw.lstrip()[end:]
notices = ['duplex linked dependency notices. Tool archives retain their bundled notices.\n']
for module in modules:
    root = Path(module['Dir'])
    files = sorted(p for p in root.iterdir() if p.is_file() and (p.name.upper().startswith('LICENSE') or p.name.upper() in ('COPYING', 'NOTICE')))
    if not files:
        raise SystemExit('Missing license: ' + module['Path'])
    notices.append('\n===== ' + module['Path'] + ' ' + module['Version'] + ' =====\n')
    notices.extend('\n' + p.name + '\n' + p.read_text() for p in files)
for name in ('go-LICENSE.txt', 'htmx-LICENSE.txt', 'deno-LICENSE.txt'):
    notices.append('\n===== ' + name + ' =====\n' + Path('docs/licenses', name).read_text())
Path('THIRD_PARTY_NOTICES.txt').write_text('\n'.join(notices))
Path('docs/go-dependencies.json').write_text(json.dumps([{'module': m['Path'], 'version': m['Version']} for m in modules], indent=2) + '\n')
print('Generated notices for', len(modules), 'linked Go modules and HTMX.')
