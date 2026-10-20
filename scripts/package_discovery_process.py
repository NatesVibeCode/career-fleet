#!/usr/bin/env python3
"""Bundle an exact new Forge development print, without a compiler or factory at runtime."""
import argparse
import hashlib
import json
import re
from pathlib import Path
import shutil
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--package', type=Path, required=True)
    args = parser.parse_args()
    repository = Path(__file__).resolve().parents[1]
    source = args.package.resolve()
    closures = json.loads((repository / 'processes/company-discovery/author/node-closure-manifest.json').read_text())
    for name, closure in closures.items():
        directory = source / 'nodes' / name / name
        if not directory.is_dir():
            parser.error(f'printed Node closure missing: {name}')
        actual = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(directory.glob('*.go')) if p.name != 'entry.go'}
        digest = hashlib.sha256(json.dumps(actual, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        if actual != closure['files'] or digest != closure['digest']:
            parser.error(f'printed Node closure changed: {name}')
        entry = (directory / 'entry.go').read_text()
        if not entry.startswith('// career-fleet-author-closure-sha256: ' + digest + '\n'):
            parser.error(f'printed Node closure pin mismatch: {name}')
    index_path = source / 'nodes/open/open/source_block_index.json'
    if not index_path.is_file():
        parser.error('verified source-selection Node catalog is missing')
    if index_path.exists():
        owner_result = source.parent / 'plan-result.json'
        if not owner_result.is_file():
            parser.error('source Block packaging requires the Forge plan result beside the package')
        pins = json.loads(owner_result.read_text())['block_pins']
        for entry in json.loads(index_path.read_text())['entries']:
            if pins.get(entry['key']) != entry['block']:
                parser.error('source Block reference differs from Forge composition: ' + entry['id'])
            artifact = json.loads((source / entry['artifact']).read_text())
            composed = json.loads((source.parent / 'blocks' / (entry['key'] + '.json')).read_text())
            if artifact != composed:
                parser.error('source Block artifact differs from Forge composition: ' + entry['id'])
    resource_root = source / 'nodes/open/open'
    match = re.search(r'^const sourceBlockMaterialJSON = (".*")$', (resource_root / 'source_block_material.go').read_text(), re.M)
    if not match:
        parser.error('source-selection resource declaration is missing')
    for name, digest in json.loads(json.loads(match.group(1))).items():
        if Path(name).name != name or hashlib.sha256((resource_root / name).read_bytes()).hexdigest() != digest:
            parser.error('source-selection resource changed: ' + name)
    development = json.loads((source / 'development.json').read_text())
    if development.get('development') is not True or development.get('admitted') is not False:
        parser.error('this packager requires an explicitly labelled unsigned development print')
    specification = json.loads((repository / 'processes/company-discovery/specs/open.json').read_text())
    schema = next(f['type'] for f in specification['fields'] if f['side'] == 'input' and f['name'] == 'request')
    destination = repository / 'career_fleet/resources/company_discovery'
    with tempfile.TemporaryDirectory() as temporary:
        prepared = Path(temporary) / 'package'
        shutil.copytree(source, prepared, ignore=shutil.ignore_patterns('bin'))
        (prepared / 'request.schema.json').write_text(json.dumps(schema, indent=2) + '\n')
        files = {p.relative_to(prepared).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(prepared.rglob('*')) if p.is_file()}
        manifest = {'schema': 'career_fleet.printed_process.v1', 'process': development['process'], 'tool': 'company_discovery', 'stage': 'development', 'admitted': False, 'files': files}
        (prepared / 'source-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        if destination.exists():
            shutil.rmtree(destination)
        shutil.copytree(prepared, destination)
    print(json.dumps(manifest['process']))


if __name__ == '__main__':
    main()
