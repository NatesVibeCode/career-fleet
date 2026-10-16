"""Real printed worker through the Career Fleet CLI and SQLite projection."""
import json
from pathlib import Path
import sqlite3
import pytest
from career_fleet.cli import main
from career_fleet.research_store import ResearchStore


@pytest.fixture(scope='module')
def built_worker(tmp_path_factory):
    # Build is an explicit lifecycle operation, not a mocked Node invocation.
    from career_fleet.process_runtime import build_worker
    return build_worker()


def test_init_requires_only_an_objective(tmp_path):
    output = tmp_path / 'request.json'
    assert main(['init', '--objective', 'ceramic filtration', '--output', str(output)]) == 0
    request = json.loads(output.read_text())['request']
    assert request['objective'] == 'ceramic filtration'
    assert request['criteria'] == []
    assert request['policy']['acquisition_enabled'] is False
    assert not (tmp_path / 'profile.json').exists()


def test_real_process_cli_store_and_export(tmp_path, monkeypatch, capsys, built_worker):
    monkeypatch.chdir(tmp_path)
    # Discovery must not open an ambient personal file.
    Path('profile.json').write_text('this must never be parsed')
    repository = Path(__file__).resolve().parents[2]
    request = repository / 'examples/company-discovery/anonymous-sparse.json'
    database = tmp_path / 'research.db'
    assert main(['discover', '--request', str(request), '--db', str(database), '--run-directory', str(tmp_path / 'execution')]) == 0
    response = json.loads(capsys.readouterr().out)
    result = response['outputs']['result']
    assert response['development'] is True
    assert result['inference_calls'] == 0
    assert result['network_requests'] == 0
    assert len(result['candidates']) == 2
    assert all(c['disposition'] != 'explicit_condition_contradicted' for c in result['candidates'])
    assert main(['list', '--db', str(database)]) == 0
    assert len(json.loads(capsys.readouterr().out)) == 2
    lead_id = result['candidates'][0]['id']
    assert main(['dossier', '--id', lead_id, '--db', str(database)]) == 0
    assert json.loads(capsys.readouterr().out)[0]['candidate']['id'] == lead_id
    output = tmp_path / 'export.json'
    assert main(['export', '--db', str(database), '--run', response['event_id'], '--output', str(output)]) == 0
    assert json.loads(output.read_text()) == response


def test_real_process_invalid_request_retains_failure(tmp_path, capsys, built_worker):
    repository = Path(__file__).resolve().parents[2]
    value = json.loads((repository / 'examples/company-discovery/anonymous-sparse.json').read_text())
    value['request']['objective'] = ''
    value['request']['seeds'] = []
    request = tmp_path / 'invalid.json'
    request.write_text(json.dumps(value))
    directory = tmp_path / 'failed-execution'
    assert main(['discover', '--request', str(request), '--db', str(tmp_path / 'failed.db'), '--run-directory', str(directory)]) != 0
    response = json.loads((directory / 'response.json').read_text())
    assert response['ok'] is False
    assert (directory / 'stderr.txt').exists()
    assert (directory / 'execution.json').exists()
    assert not response.get('outputs', {}).get('result', {}).get('candidates')


def test_projection_does_not_merge_domains_or_delete_on_empty_run(tmp_path):
    database = tmp_path / 'research.db'
    # Existing career data is retained while new Process outcomes are appended.
    with sqlite3.connect(database) as connection:
        connection.execute('CREATE TABLE companies(id TEXT PRIMARY KEY)')
        connection.execute("INSERT INTO companies VALUES ('existing')")
    store = ResearchStore(database)
    store.record({'event_id': 'one', 'outputs': {'result': {'candidates': [{'id': 'mention:a', 'urls': ['https://shared.example']}, {'id': 'mention:b', 'urls': ['https://shared.example']}]}}})
    store.record({'event_id': 'source-failed', 'outputs': {'result': {'candidates': []}}})
    assert len(store.leads()) == 2
    assert store.connection.execute('SELECT id FROM companies').fetchone()[0] == 'existing'
    with pytest.raises(sqlite3.IntegrityError):
        store.record({'event_id': 'one', 'outputs': {}})
    store.close()
