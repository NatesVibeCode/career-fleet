"""Real printed-worker lifecycle, idempotency and evidence replay regressions."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import pytest
from career_fleet import campaign, operations
from career_fleet.process_runtime import build_worker, checked_execution
from career_fleet.research_store import ResearchStore


@pytest.fixture(scope='module', autouse=True)
def worker():
    build_worker()


def offline_request(tmp_path):
    repository = Path(__file__).resolve().parents[2]
    payload = json.loads((repository / 'examples/company-discovery/anonymous-sparse.json').read_text())
    request = tmp_path / 'request.json'
    request.write_text(json.dumps(payload))
    return request


def test_campaign_resume_reuses_exact_event_and_store_is_idempotent(tmp_path):
    request = offline_request(tmp_path)
    directory, db = tmp_path / 'campaign', tmp_path / 'research.db'
    first, code = operations.campaign_operation('run', directory, db, request)
    assert code == 0 and first['ok']
    _, run = campaign.current(directory)
    launch = checked_execution(run)
    assert launch['event_id'] == first['event_id']
    second, code = operations.campaign_operation('resume', directory, db)
    assert code == 0 and second['event_id'] == first['event_id']
    assert second['execution_mode'] == 'retained_event'
    assert first['outputs']['result'] == second['outputs']['result']
    store = ResearchStore(db)
    try:
        assert store.connection.execute('SELECT COUNT(*) FROM process_runs').fetchone()[0] == 1
        assert len(store.leads()) == len(first['outputs']['result']['candidates'])
    finally:
        store.close()
    assert len(list((run / 'attempts').iterdir())) == 2


def test_resume_refuses_input_and_package_drift(tmp_path):
    request = offline_request(tmp_path)
    directory = tmp_path / 'campaign'
    campaign.start(request, directory)
    _, run = campaign.current(directory)
    original = (run / 'input.json').read_bytes()
    (run / 'input.json').write_bytes(original + b' ')
    with pytest.raises(ValueError, match='binding changed'):
        campaign.continue_campaign(directory)
    (run / 'input.json').write_bytes(original)
    launch = json.loads((run / 'execution.json').read_text())
    launch['worker_sha256'] = 'changed'
    (run / 'execution.json').write_text(json.dumps(launch))
    with pytest.raises(ValueError, match='worker changed'):
        campaign.continue_campaign(directory)


def test_replay_uses_raw_captures_without_network(tmp_path):
    import base64
    body = b'<table><tr><th>Company</th></tr><tr><td><a href="https://acme.example/">Acme</a></td></tr></table>'
    request = operations.request_template('industrial suppliers')
    request['sources'] = [{'id': 'directory.test', 'kind': 'collection_html', 'preset': 'html_table', 'family': 'suppliers', 'url_template': 'https://directory.example/', 'queries': ['{objective}'], 'record_kind': 'organization'}]
    request['reusable_captures'] = [{'id': 'capture:test', 'url': 'https://directory.example/', 'final_url': 'https://directory.example/', 'captured_at': '2026-10-16T00:00:00Z', 'status': 200, 'media_type': 'text/html', 'body_base64': base64.b64encode(body).decode(), 'sha256': hashlib.sha256(body).hexdigest(), 'outcome': 'success', 'error_code': '', 'redirect_location': '', 'receipt_refs': [], 'complete': True, 'requests': 0}]
    path = tmp_path / 'request.json'
    path.write_text(json.dumps({'request': request}))
    directory = tmp_path / 'campaign'
    first, code = campaign.start(path, directory)
    assert code == 0 and first['ok']
    second, code = campaign.rerun(directory, 'replay')
    assert code == 0 and second['ok']
    assert second['event_id'] != first['event_id']
    assert second['outputs']['result']['network_requests'] == 0
    assert len(second['outputs']['result']['candidates']) == 1
    assert campaign.status(directory)['executions'][-1]['kind'] == 'replay'


def test_failed_attempt_does_not_block_later_settlement(tmp_path):
    store = ResearchStore(tmp_path / 'research.db')
    try:
        store.record({'event_id': 'event', 'ok': False, 'error': 'interrupted'})
        result = {'candidates': [{'id': 'one'}]}
        store.record({'event_id': 'event', 'ok': True, 'outputs': {'result': result}})
        store.record({'event_id': 'event', 'ok': True, 'execution_mode': 'retained_event', 'outputs': {'result': result}})
        assert len(store.leads()) == 1
        assert len(store.dossier('one')) == 1
        with pytest.raises(ValueError, match='settled Process result changed'):
            store.record({'event_id': 'event', 'outputs': {'result': {'candidates': []}}})
    finally:
        store.close()


def test_launch_manifest_survives_interruption_before_worker_return(tmp_path, monkeypatch):
    import career_fleet.process_runtime as runtime
    request = offline_request(tmp_path)
    directory = tmp_path / 'run'
    original = runtime.subprocess.run
    def interrupted(*args, **kwargs):
        assert (directory / 'execution.json').is_file()
        assert (directory / 'execution.key').is_file()
        checked_execution(directory)
        raise KeyboardInterrupt()
    monkeypatch.setattr(runtime.subprocess, 'run', interrupted)
    with pytest.raises(KeyboardInterrupt):
        runtime.execute(request, directory)
    monkeypatch.setattr(runtime.subprocess, 'run', original)
    response, code = runtime.resume(directory)
    assert code == 0 and response['ok']


def test_refresh_after_replay_uses_origin_authority_and_current_print(tmp_path, monkeypatch):
    import career_fleet.campaign as module
    request = offline_request(tmp_path)
    payload = json.loads(request.read_text())
    payload['request']['policy']['acquisition_enabled'] = True
    request.write_text(json.dumps(payload))
    directory = tmp_path / 'campaign'
    # No sources are acquired in this supplied-record request.
    first, code = campaign.start(request, directory)
    assert code == 0
    state, original = campaign.current(directory)
    # Emulate the replay custody input: acquisition is disabled in that event.
    replay_payload = json.loads((original / 'input.json').read_text())
    replay_payload['request']['policy']['acquisition_enabled'] = False
    replay_input = tmp_path / 'replay.json'
    replay_input.write_text(json.dumps(replay_payload))
    from career_fleet.process_runtime import execute
    replay_run = directory / 'executions' / 'replay'
    execute(replay_input, replay_run)
    state['current'] = str(replay_run)
    state['executions'].append({'directory':str(replay_run),'kind':'replay'})
    (directory / 'campaign.json').write_text(json.dumps(state))
    captured = {}
    def observe(path, run, binding, **kwargs):
        captured.update(request=json.loads(path.read_text())['request'], kwargs=kwargs)
        return {'ok': True}, 0
    monkeypatch.setattr(module, 'execute', observe)
    campaign.rerun(directory, 'refresh')
    assert captured['request']['policy']['acquisition_enabled'] is True
    assert captured['kwargs']['pinned_source'] is None


def test_source_registry_preserves_new_failures_and_block_history(tmp_path):
    store = ResearchStore(tmp_path / 'research.db')
    health = {'source_id':'s','source_digest':'revision','url':'https://directory.example/','outcome':'working','records':3}
    try:
        store.record({'event_id':'first','ok':True,'outputs':{'result':{'source_health':[health]}}})
        store.record({'event_id':'later','ok':True,'outputs':{'result':{'source_health':[{**health,'outcome':'blocked','records':0}]}}})
        assert store.source_health() == [{**health,'outcome':'blocked','records':0,'event_id':'later'}]
        assert store.run('first')['outputs']['result']['source_health'][0]['outcome'] == 'working'
    finally:
        store.close()


def test_reverify_preserves_retained_known_mapping(tmp_path, monkeypatch):
    source = {'id':'collection:known','url_template':'https://directory.example/','kind':'collection_html','preset':'mapped_html',
              'html_mapping':{'item_selector':'article','title_selector':'h2','url_selector':'a'},'queries':['{objective}'],'pages':1}
    path = tmp_path / 'candidate.json'
    path.write_text(json.dumps({'id':'source-candidate:known','url':source['url_template'],'proposed_source':source}))
    captured = {}
    def observe(request, *args, **kwargs):
        captured.update(json.loads(request.read_text()))
        return {'ok':True},0
    monkeypatch.setattr(operations, 'discover', observe)
    operations.inspect_source(path,'auto_collection',tmp_path / 'research.db',tmp_path / 'run')
    selected = captured['request']['sources'][0]
    assert selected['preset']=='mapped_html' and selected['html_mapping']==source['html_mapping']
