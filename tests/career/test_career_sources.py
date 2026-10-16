"""The new discovery boundary has an explicit source plan and sealed source inventory."""
import json
from pathlib import Path
import shutil
import pytest
from career_fleet.cli import main
import career_fleet.process_runtime as runtime


def test_initial_request_has_no_automatic_sources(tmp_path):
    path = tmp_path / 'request.json'
    assert main(['init', '--objective', 'water filtration', '--output', str(path)]) == 0
    request = json.loads(path.read_text())['request']
    assert request['sources'] == []
    assert request['criteria'] == []
    assert request['questions'] == []
    assert request['policy']['acquisition_enabled'] is False
    assert request['reusable_captures'] == []


def test_old_source_preset_flags_are_not_compatibility_aliases():
    with pytest.raises(SystemExit):
        main(['discover', '--source', 'community'])


def test_exact_printed_source_inventory():
    package, manifest = runtime.checked_manifest()
    assert manifest['process']['name'] == 'company_discovery.v1'
    assert manifest['stage'] == 'development'
    assert manifest['admitted'] is False
    assert (package / 'licenses/golang-x-net-LICENSE').is_file()
    assert (package / 'process.json').is_file()


def test_changed_worker_source_refuses_before_build_or_execution(tmp_path, monkeypatch):
    package = tmp_path / 'package'
    shutil.copytree(runtime.package_path(), package)
    (package / 'worker/main.go').write_text('package changed')
    monkeypatch.setattr(runtime, 'package_path', lambda: package)
    with pytest.raises(ValueError, match='printed Process source changed'):
        runtime.checked_manifest()
