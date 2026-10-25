"""Inspect and export retained Career Fleet records."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import sqlite3
import sys
from career_fleet import __version__
from career_fleet.research_store import ResearchStore

def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', action='version', version=__version__)
    commands = parser.add_subparsers(dest='command', required=True)
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
    args = parser.parse_args(argv)
    args.db = args.db.expanduser()
    store = None
    try:
        if not args.db.is_file():
            raise ValueError(f'no research database: {args.db}')
        store = ResearchStore(args.db, readonly=True)
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
    except (OSError, ValueError, RuntimeError, sqlite3.Error) as exc:
        print(f'career-fleet: {exc}', file=sys.stderr)
        return 1
    finally:
        if store is not None:
            store.close()

if __name__ == '__main__':
    raise SystemExit(main())
