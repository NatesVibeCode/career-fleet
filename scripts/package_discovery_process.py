#!/usr/bin/env python3
"""Bundle an exact new Forge development print, without a compiler or factory at runtime."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--package', type=Path, required=True)
    args = parser.parse_args()
    repository = Path(__file__).resolve().parents[1]
    source = args.package.resolve()
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
