"""Shared CLI/MCP lifecycle operations; no acquisition or business scheduling."""
from __future__ import annotations
import json
from pathlib import Path
import uuid
from career_fleet import campaign
from career_fleet.channel_catalog import default_sources
from career_fleet.process_runtime import execute, package_path, _atomic_json
from career_fleet.research_store import ResearchStore


def request_template(objective: str) -> dict:
    from career_fleet.cli import empty_request
    request = empty_request(json.loads((package_path() / 'request.schema.json').read_text()))
    request['objective'] = objective
    return request


def initialize(objective: str, output: Path, *, pages: int = 1, enable_browser: bool = False,
               enable_acquisition: bool = False, max_requests: int | None = None,
               empty_sources: bool = False, source_ids: list[str] | None = None,
               families: list[str] | None = None) -> Path:
    if pages < 1:
        raise ValueError('pages must be at least one')
    if max_requests is not None and (max_requests < 0 or not enable_acquisition):
        raise ValueError('max_requests requires acquisition and a nonnegative value')
    if empty_sources and (source_ids or families):
        raise ValueError('empty_sources cannot select sources or families')
    request = request_template(objective)
    from career_fleet.channel_catalog import source_blocks_catalog
    values = default_sources(pages, enable_browser=enable_browser)
    blocks = source_blocks_catalog()
    requested = set(source_ids or [])
    known = values + blocks
    unknown = requested - {value['id'] for value in known}
    unknown_families = set(families or []) - {value.get('family', '') for value in known}
    if unknown or unknown_families:
        raise ValueError('unknown source/family: ' + ', '.join(sorted(unknown | unknown_families)))
    def selected(value):
        return not empty_sources and ((not requested and not families) or value['id'] in requested or value.get('family') in (families or []))
    request['sources'] = [source for source in values if selected(source)]
    request['source_blocks'] = [{'block':entry['block'], 'pages':pages} for entry in blocks if selected(entry)]
    request['policy']['acquisition_enabled'] = enable_acquisition
    if max_requests is not None:
        request['policy']['max_requests'] = max_requests
    with output.open('x', encoding='utf-8') as stream:
        json.dump({'request': request}, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    return output


def retain(response: dict, db: Path) -> dict:
    if response.get('event_id'):
        db.parent.mkdir(parents=True, exist_ok=True)
        store = ResearchStore(db)
        try:
            store.record(response)
        finally:
            store.close()
    return response


def discover(request: Path, db: Path, directory: Path, browser_runtime: Path | None = None) -> tuple[dict, int]:
    response, code = execute(request, directory, browser_runtime)
    return retain(response, db), code


def discover_sources(request_path: Path, db: Path, directory: Path, browser_runtime: Path | None = None) -> tuple[dict, int]:
    payload = json.loads(request_path.read_text())
    request = payload.get('request', payload)
    request['mode'] = 'sources'
    wrapper = directory.parent / ('source-request-' + uuid.uuid4().hex + '.json')
    wrapper.parent.mkdir(parents=True, exist_ok=True)
    _atomic_json(wrapper, {'request': request})
    return discover(wrapper, db, directory, browser_runtime)


def inspect_source(candidate_path: Path, preset: str, db: Path, directory: Path) -> tuple[dict, int]:
    candidate_path = Path(candidate_path)
    if candidate_path.is_file():
        candidate = json.loads(candidate_path.read_text())
    else:
        store = ResearchStore(db)
        try:
            candidate = store.source_candidate(str(candidate_path))
        finally:
            store.close()
    request = request_template('Verify the supplied business collection')
    request['mode'] = 'verify'
    if candidate.get('block'):
        if preset != 'auto_collection':
            raise ValueError('a pinned Block mapping is immutable; author a new Block revision through Forge')
        request['source_blocks'] = [{'block': candidate['block']}]
    else:
        source = candidate.get('proposed_source') or {'id': candidate['id'], 'kind': 'collection_html', 'url_template': candidate['url'], 'queries': ['{objective}'], 'family': candidate.get('family', ''), 'pages': 1}
        selected_preset = source.get('preset', preset) if preset == 'auto_collection' else preset
        request['sources'] = [dict(source, preset=selected_preset, source_digest='')]
    request['policy']['acquisition_enabled'] = True
    request_path = directory.parent / ('verify-request-' + uuid.uuid4().hex + '.json')
    request_path.parent.mkdir(parents=True, exist_ok=True)
    _atomic_json(request_path, {'request': request})
    return discover(request_path, db, directory)


def sources(db: Path | None = None) -> list[dict]:
    from career_fleet.channel_catalog import source_blocks_catalog
    values = [{'source': source, 'status': 'configured_unverified'} for source in default_sources()] + source_blocks_catalog()
    if db and db.exists():
        store = ResearchStore(db)
        try:
            health = store.source_health()
            for value in values:
                value['latest_health'] = [item for item in health if item['source_id'] == (value.get('id') or value.get('source', {}).get('id'))]
            values.extend(store.source_candidates())
        finally:
            store.close()
    return values


def blocks(db: Path | None = None) -> list[dict]:
    from career_fleet.channel_catalog import source_blocks_catalog
    values = source_blocks_catalog()
    if db and db.exists():
        store = ResearchStore(db)
        try:
            for candidate in store.blocks():
                for entry in values:
                    if entry['block'] == candidate['block']:
                        entry['local_verification'] = candidate
        finally:
            store.close()
    return values


def verify_block(identifier: str, db: Path, directory: Path) -> tuple[dict, int]:
    from career_fleet.channel_catalog import source_blocks_catalog
    entry = next((entry for entry in source_blocks_catalog() if identifier in {entry['id'], entry['block']['name'] + '@' + entry['block']['digest']}), None)
    if entry is None:
        raise ValueError('unknown source Block: ' + identifier)
    request = request_template('Verify this pinned source Block')
    request.update(mode='verify', source_blocks=[{'block':entry['block']}])
    request['policy'].update(acquisition_enabled=True, follow_redirects=True)
    path = directory.parent / ('block-request-' + uuid.uuid4().hex + '.json')
    path.parent.mkdir(parents=True, exist_ok=True)
    _atomic_json(path, {'request':request})
    return discover(path, db, directory)


def campaign_operation(operation: str, directory: Path, db: Path, request: Path | None = None, browser_runtime: Path | None = None) -> tuple[dict, int]:
    if operation == 'status':
        return campaign.status(directory), 0
    if operation == 'run':
        if request is None:
            raise ValueError('campaign run requires --request')
        response, code = campaign.start(request, directory, browser_runtime)
    elif operation == 'resume':
        response, code = campaign.continue_campaign(directory)
    else:
        response, code = campaign.rerun(directory, operation)
    return retain(response, db), code
