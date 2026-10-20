#!/usr/bin/env python3
"""Lower exact pure core source and refresh typed authoring declarations.

This prepares source material only. Forge plan remains the Process printer.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
process = root / 'processes/company-discovery'
core = process / 'author/core'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--forge-root', type=Path, required=True)
    args = parser.parse_args()
    env = dict(os.environ, GOWORK='off')
    schemas = json.loads(subprocess.check_output(['go', 'run', str(root / 'scripts/discovery-schema/main.go')], cwd=core, env=env))
    tokenizer = process / 'author/tokenizer'
    selected = list(core.glob('*.go'))
    selected = [p for p in selected if not p.name.endswith('_test.go')]
    selected += list(tokenizer.glob('*.go'))
    html_prefix = 'fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_'
    for directory in (process / 'nodes').iterdir():
        if not directory.is_dir() or directory.name in {'browsercapture', 'browsercapturerecover'}:
            continue
        for source in selected:
            text = re.sub(r'^package \w+[^\n]*', f'package {directory.name}', source.read_text(), count=1, flags=re.M)
            if source.parent == core:
                text = text.replace('\t"golang.org/x/net/html"\n', '')
                text = re.sub(r'\bhtml\.([A-Za-z_][A-Za-z0-9_]*)', lambda match: html_prefix + match[1], text)
            (directory / source.name).write_text(text)
    manifest = {str(p.relative_to(process)): hashlib.sha256(p.read_bytes()).hexdigest() for p in selected}
    (process / 'author/closure-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    node_closures = {}
    for directory in (process / 'nodes').iterdir():
        if not directory.is_dir() or directory.name in {'browsercapture', 'browsercapturerecover'}:
            continue
        entry = directory / 'entry.go'
        if entry.exists():
            text = re.sub(r'^// career-fleet-author-closure-sha256: [0-9a-f]+\n', '', entry.read_text(), flags=re.M)
            files = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(directory.glob('*.go')) if p.name != 'entry.go' and not p.name.endswith('_test.go')}
            closure_digest = hashlib.sha256(json.dumps(files, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
            entry.write_text('// career-fleet-author-closure-sha256: ' + closure_digest + '\n' + text)
            node_closures[directory.name] = {'files': files, 'digest': closure_digest}
    (process / 'author/node-closure-manifest.json').write_text(json.dumps(node_closures, indent=2, sort_keys=True) + '\n')
    for path in (process / 'specs').glob('*.json'):
        spec = json.loads(path.read_text())
        for field in spec['fields']:
            if field['name'] in schemas:
                field['type'] = schemas[field['name']]
        entry = process / 'nodes' / path.stem / 'entry.go'
        digest = hashlib.sha256(b'praxis.source.go.v1\0' + entry.read_bytes()).hexdigest()
        mechanism = spec['mechanism']
        if 'code' in mechanism:
            mechanism['code']['source_digest'] = digest
        if 'capability' in mechanism:
            mechanism['capability']['adapter']['digest'] = digest
        if path.stem == 'browsercapture':
            program = re.search(r'const browserSearchProgram = `(.*?)`', entry.read_text(), re.S).group(1)
            for dimension in spec['dimensions']:
                for operation in (dimension.get('contents') or {}).get('operations', []):
                    if 'browser' in operation:
                        operation['browser']['program_sha256'] = hashlib.sha256(program.encode()).hexdigest()
        path.write_text(json.dumps(spec, indent=2) + '\n')
    stores_path = process / 'stores.json'
    stores = json.loads(stores_path.read_text())
    for pointer in stores['pointers']:
        if pointer.get('kind') in {'code', 'adapter'}:
            pointer['digest'] = hashlib.sha256(b'praxis.source.go.v1\0' + (process / pointer['path']).read_bytes()).hexdigest()
    # Proof tests are source-bound declarations, not execution observations.
    for path in (process / 'specs').glob('*.json'):
        spec = json.loads(path.read_text())
        test = spec.get('proof_spec', {}).get('test', {})
        file = process / 'nodes' / path.stem / 'proof_test.go'
        if test.get('pointer') and file.exists():
            stores['pointers'] = [p for p in stores['pointers'] if not (p.get('kind') == 'test' and p.get('pointer') == test['pointer'])]
            stores['pointers'].append({'kind':'test','pointer':test['pointer'],'path':str(file.relative_to(process)), 'digest':hashlib.sha256(b'praxis.test.go.v1\0'+file.read_bytes()).hexdigest()})
    stores_path.write_text(json.dumps(stores, indent=2) + '\n')
    subprocess.run(['go', 'run', str(root / 'scripts/discovery-recovery/main.go'), str(process / 'plan.json')], cwd=args.forge_root, check=True)
    print('Lowered pure source and refreshed Request/State/Result/Capture contracts. Reauthor recovery references and print through Forge plan.')


if __name__ == '__main__':
    main()
