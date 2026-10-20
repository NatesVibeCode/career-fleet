"""Lifecycle custody only: the printed Process owns campaign work and recovery."""
from __future__ import annotations
import json
from pathlib import Path
import uuid
from career_fleet.process_runtime import _atomic_json, checked_execution, execute, resume


def start(request: Path, directory: Path, browser_runtime: Path | None = None) -> tuple[dict, int]:
    directory = directory.resolve()
    directory.mkdir(parents=True, mode=0o700, exist_ok=False)
    run = directory / 'executions' / uuid.uuid4().hex
    state = {'schema': 'career_fleet.campaign.v1', 'current': str(run), 'executions': [{'directory': str(run), 'kind': 'run'}], 'browser_runtime': str(browser_runtime.resolve()) if browser_runtime else None}
    _atomic_json(directory / 'campaign.json', state)
    return execute(request, run, browser_runtime)


def current(directory: Path) -> tuple[dict, Path]:
    directory = directory.resolve()
    state = json.loads((directory / 'campaign.json').read_text())
    if state.get('schema') != 'career_fleet.campaign.v1':
        raise ValueError('unsupported campaign manifest')
    run = Path(state['current']).resolve()
    if not run.is_relative_to(directory / 'executions'):
        raise ValueError('campaign execution escapes its execution directory')
    return state, run


def continue_campaign(directory: Path) -> tuple[dict, int]:
    _, run = current(directory)
    return resume(run)


def status(directory: Path) -> dict:
    state, run = current(directory)
    launch = checked_execution(run)
    response_path = run / 'response.json'
    response = json.loads(response_path.read_text()) if response_path.exists() else {}
    result = response.get('outputs', {}).get('result', {})
    return {'campaign': str(directory.resolve()), 'execution': str(run), 'event_id': launch['event_id'],
            'process': launch['process'], 'state': 'settled' if response.get('outputs', {}).get('result') is not None else ('attempt_failed' if response else 'no_settled_response'),
            'ok': response.get('ok'), 'summary': result.get('summary', {}), 'source_health': result.get('source_health', []),
            'actions': result.get('actions', []), 'executions': state['executions']}


def rerun(directory: Path, kind: str) -> tuple[dict, int]:
    if kind not in {'refresh', 'replay'}:
        raise ValueError('rerun must be refresh or replay')
    state, previous = current(directory)
    launch = checked_execution(previous)
    request_source = previous
    if kind == 'refresh':
        request_source = Path(state['executions'][0]['directory']).resolve()
        if not request_source.is_relative_to(directory.resolve() / 'executions'):
            raise ValueError('campaign origin escapes its execution directory')
        checked_execution(request_source)
    payload = json.loads((request_source / 'input.json').read_text())
    request = payload.get('request', payload)
    response_path = previous / 'response.json'
    response = json.loads(response_path.read_text()) if response_path.exists() else {}
    result = response.get('outputs', {}).get('result', {})
    if kind == 'replay':
        captures = {}
        for capture in result.get('captures', []):
            if capture.get('complete') and capture.get('outcome') == 'success':
                captures[capture['url']] = capture
        if not captures:
            raise ValueError('replay requires retained successful captures')
        request['reusable_captures'] = list(captures.values())
        request['policy']['acquisition_enabled'] = False
    else:
        request['reusable_captures'] = []
        request['prior'] = result.get('candidates', request.get('prior', []))
    # New labels and events never rewrite the selected source execution.
    input_path = directory.resolve() / ('request-' + uuid.uuid4().hex + '.json')
    _atomic_json(input_path, {'request': request})
    run = directory.resolve() / 'executions' / uuid.uuid4().hex
    new_state = {**state, 'current': str(run), 'executions': state['executions'] + [{'directory': str(run), 'kind': kind, 'source_event_id': launch['event_id']}]}
    # Replay pins the source print; refresh selects the current print. Neither
    # relabels the original event or silently changes its Block semantics.
    _atomic_json(directory.resolve() / 'campaign.json', new_state)
    binding = Path(state['browser_runtime']) if state.get('browser_runtime') else None
    return execute(input_path, run, binding, evidence_source=previous / 'evidence' if kind == 'replay' and (previous / 'evidence').exists() else None, pinned_source=previous if kind == 'replay' else None)
