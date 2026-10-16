"""Career Fleet: explicit research requests executed by a printed Process."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import uuid
from career_fleet import __version__
from career_fleet.process_runtime import build_worker, execute, package_path
from career_fleet.research_store import ResearchStore
from career_fleet.setup import install_skill


def empty_request(schema: dict):
    kind = schema['type']
    if kind == 'object':
        return {name: empty_request(schema['properties'][name]) for name in schema['required']}
    if kind == 'array':
        return []
    return {'string': '', 'integer': 0, 'boolean': False}[kind]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', action='version', version=__version__)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('build', help='Compile the bundled standalone discovery worker locally')
    init = commands.add_parser('init', help='Write an explicit, empty research request')
    init.add_argument('--objective', required=True)
    init.add_argument('--output', type=Path, default=Path('research-request.json'))
    discover = commands.add_parser('discover', help='Execute the deterministic discovery and evidence Process')
    discover.add_argument('--request', type=Path, required=True)
    discover.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    discover.add_argument('--run-directory', type=Path)
    leads = commands.add_parser('list', help='List every retained lead, including unresolved and deferred leads')
    leads.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    leads.add_argument('--run', help='Select one exact execution')
    dossier = commands.add_parser('dossier', help='Show all recorded versions of an exact lead')
    dossier.add_argument('--id', required=True)
    dossier.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    export = commands.add_parser('export', help='Export retained leads or one complete execution')
    export.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    export.add_argument('--run')
    export.add_argument('--output', type=Path, required=True)
    setup = commands.add_parser('setup', help='Install the bundled Career Fleet assistant instructions')
    setup.add_argument('--workspace-root', type=Path, default=Path.cwd())
    setup.add_argument('--force', action='store_true')
    args = parser.parse_args(argv)
    store = None
    try:
        if args.command == 'build':
            print(build_worker())
            return 0
        if args.command == 'init':
            schema = json.loads((package_path() / 'request.schema.json').read_text())
            request = empty_request(schema)
            request['objective'] = args.objective
            with args.output.open('x', encoding='utf-8') as output:
                json.dump({'request': request}, output, indent=2, ensure_ascii=False)
                output.write('\n')
            print(args.output)
            return 0
        if args.command == 'setup':
            print(json.dumps(install_skill(args.workspace_root, force=args.force)))
            return 0
        if args.command == 'discover':
            directory = (args.run_directory or Path('runs') / uuid.uuid4().hex).expanduser().resolve()
            response, status = execute(args.request.expanduser().resolve(), directory)
            store = ResearchStore(args.db.expanduser())
            if response.get('event_id'):
                store.record(response)
            print(json.dumps(response, ensure_ascii=False))
            print(f'Execution artifacts: {directory}', file=sys.stderr)
            return status if status else (0 if response.get('ok') else 1)
        if not args.db.is_file():
            raise ValueError(f'no research database: {args.db}')
        store = ResearchStore(args.db.expanduser())
        if args.command == 'dossier':
            output = store.dossier(args.id)
            if not output:
                raise ValueError(f'unknown lead: {args.id}')
        elif args.command == 'export' and args.run:
            output = store.run(args.run)
        else:
            output = store.leads(args.run)
        if args.command == 'export':
            with args.output.open('x', encoding='utf-8') as stream:
                json.dump(output, stream, ensure_ascii=False, indent=2)
                stream.write('\n')
        else:
            print(json.dumps(output, ensure_ascii=False, indent=2))
        return 0
    except (OSError, ValueError, RuntimeError, sqlite3.Error, subprocess.CalledProcessError) as exc:
        print(f'career-fleet: {exc}', file=sys.stderr)
        return 1
    finally:
        if store is not None:
            store.close()


if __name__ == '__main__':
    raise SystemExit(main())
