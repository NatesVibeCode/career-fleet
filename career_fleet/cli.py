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
from career_fleet.channel_catalog import default_sources
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
    init = commands.add_parser('init', help='Write a generic request with broad public discovery channels')
    init.add_argument('--objective', required=True)
    init.add_argument('--output', type=Path, default=Path('research-request.json'))
    init.add_argument('--source', action='append', default=[], help='Select a source ID; repeat for multiple sources')
    init.add_argument('--family', action='append', default=[], help='Select a source population; repeat for multiple populations')
    init.add_argument('--empty-sources', action='store_true', help='Start without the broad generic source catalog')
    init.add_argument('--pages', type=int, default=1, help='Pages per paginated channel; no hard maximum')
    init.add_argument('--enable-browser', action='store_true', help='Use the pinned local browser for JavaScript-rendered marketplace channels')
    init.add_argument('--enable-acquisition', action='store_true', help='Allow public source reads when this request runs')
    init.add_argument('--max-requests', type=int, help='Optional explicit total read budget for this run')
    discover = commands.add_parser('discover', help='Execute the deterministic discovery and evidence Process')
    discover.add_argument('--request', type=Path, required=True)
    discover.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    discover.add_argument('--run-directory', type=Path)
    discover.add_argument('--browser-runtime', type=Path, help='JSON paths for the locally installed Node, Playwright, and Chromium runtime')
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
    sources = commands.add_parser('sources', help='Discover and inspect potential collection sources')
    source_commands = sources.add_subparsers(dest='source_command', required=True)
    for name in ('list', 'discover', 'inspect'):
        command = source_commands.add_parser(name)
        command.add_argument('--db', type=Path, default=Path('career_fleet.db'))
        if name != 'list':
            command.add_argument('--run-directory', type=Path)
        if name == 'discover':
            command.add_argument('--request', type=Path, required=True)
            command.add_argument('--browser-runtime', type=Path)
        if name == 'inspect':
            command.add_argument('--candidate', required=True, help='Retained source-candidate ID or JSON file')
            command.add_argument('--preset', default='auto_collection', choices=['auto_collection', 'html_table', 'jsonld_organizations', 'mapped_html', 'mapped_json', 'ocds_suppliers'])
    blocks = commands.add_parser('blocks', help='Inspect and verify exact published source Blocks')
    block_commands = blocks.add_subparsers(dest='block_command', required=True)
    for name in ('list', 'verify'):
        command = block_commands.add_parser(name)
        command.add_argument('--db', type=Path, default=Path('career_fleet.db'))
        if name == 'verify':
            command.add_argument('--id', required=True, help='Source Block catalog ID or exact name@digest')
            command.add_argument('--run-directory', type=Path)
    campaign = commands.add_parser('campaign', help='Persist, inspect and continue Process executions')
    campaign_commands = campaign.add_subparsers(dest='campaign_command', required=True)
    for name in ('run', 'status', 'resume', 'replay', 'refresh'):
        command = campaign_commands.add_parser(name)
        command.add_argument('--directory', type=Path, required=True)
        command.add_argument('--db', type=Path, default=Path('career_fleet.db'))
        if name == 'run':
            command.add_argument('--request', type=Path, required=True)
            command.add_argument('--browser-runtime', type=Path)
    serve = commands.add_parser('serve', help='Serve the Career Fleet Process through stdio MCP')
    serve.add_argument('--workspace-root', type=Path, default=Path.cwd())
    serve.add_argument('--db', type=Path, default=Path('career_fleet.db'))
    args = parser.parse_args(argv)
    store = None
    try:
        if args.command == 'serve':
            from career_fleet.mcp_server import create_mcp_server
            create_mcp_server(args.workspace_root, args.db).run(transport='stdio')
            return 0
        if args.command == 'blocks':
            from career_fleet import operations
            if args.block_command == 'list':
                print(json.dumps(operations.blocks(args.db.expanduser()), ensure_ascii=False, indent=2))
                return 0
            directory = (args.run_directory or Path('runs') / uuid.uuid4().hex).expanduser().resolve()
            response, status = operations.verify_block(args.id, args.db.expanduser(), directory)
            print(json.dumps(response, ensure_ascii=False))
            return status if status else (0 if response.get('ok') else 1)
        if args.command in {'campaign', 'sources'}:
            from career_fleet import operations
            if args.command == 'campaign':
                response, status = operations.campaign_operation(args.campaign_command, args.directory.expanduser().resolve(), args.db.expanduser(), request=getattr(args, 'request', None), browser_runtime=getattr(args, 'browser_runtime', None))
            elif args.source_command == 'list':
                print(json.dumps(operations.sources(args.db.expanduser()), ensure_ascii=False, indent=2))
                return 0
            else:
                directory = (args.run_directory or Path('runs') / uuid.uuid4().hex).expanduser().resolve()
                if args.source_command == 'discover':
                    response, status = operations.discover_sources(args.request.expanduser().resolve(), args.db.expanduser(), directory, args.browser_runtime)
                else:
                    response, status = operations.inspect_source(Path(args.candidate).expanduser(), args.preset, args.db.expanduser(), directory)
            print(json.dumps(response, ensure_ascii=False))
            return status if status else (0 if response.get('ok', True) else 1)
        if args.command == 'build':
            print(build_worker())
            return 0
        if args.command == 'init':
            from career_fleet.operations import initialize
            print(initialize(args.objective, args.output, pages=args.pages, enable_browser=args.enable_browser,
                             enable_acquisition=args.enable_acquisition, max_requests=args.max_requests,
                             empty_sources=args.empty_sources, source_ids=args.source, families=args.family))
            return 0
        if args.command == 'setup':
            print(json.dumps(install_skill(args.workspace_root, force=args.force)))
            return 0
        if args.command == 'discover':
            directory = (args.run_directory or Path('runs') / uuid.uuid4().hex).expanduser().resolve()
            response, status = execute(
                args.request.expanduser().resolve(), directory,
                browser_runtime=args.browser_runtime.expanduser().resolve() if args.browser_runtime else None,
            )
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
