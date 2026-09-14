"""Fresh-install and real-campaign safety regressions."""
import json
from pathlib import Path

import pytest

from harness_fleet.catalog import RouteCatalog
from harness_fleet.cli import build_parser, cmd_mcp_install
from harness_fleet.input_data import InputDataError, load_input_items
from harness_fleet.models import RoutePolicy
from harness_fleet.packer import pack_items
from harness_fleet.store import HarnessStore
from harness_fleet.task import create_task_from_preset


def test_demo_is_never_selected_for_real_campaigns(tmp_path):
    catalog = RouteCatalog(db_path=tmp_path / "fleet.db")
    catalog.add_route("demo/fake", "demo", 0, 0)
    catalog.add_route("ollama/model", "ollama", 0, 0)
    assert catalog.get_ladder() == ["ollama/model"]
    assert catalog.get_ladder(policy=RoutePolicy(allowed_routes=["demo/fake"])) == ["demo/fake"]


def test_repeated_rate_limits_do_not_exhaust_batch_budget(tmp_path):
    store = HarnessStore(tmp_path / "fleet.db")
    spec = create_task_from_preset("test")
    revision = store.register_task(spec)
    store.create_run(run_id="run", task_revision_id=revision, input_path="test", input_digest="a" * 64,
                     total_items=1, max_attempts=2, batch_size=1, output_path=str(tmp_path / "out.json"))
    store.enqueue_batches("run", pack_items([{"item_id": "a", "text": "some useful source text"}]), 1)
    for _ in range(5):
        lease = store.lease_batch("run", "worker")
        assert lease is not None
        store.release_lease("run", lease["attempt_id"], "worker", "Rate limited")
    assert store.lease_batch("run", "worker") is not None
    assert store.reset_leased_batches("run") == 0
    with store.connect() as connection:
        assert connection.execute("SELECT count(*) FROM batch_attempts").fetchone()[0] == 6


def test_missing_survivor_file_fails_instead_of_silently_emptying_campaign(tmp_path):
    source = tmp_path / "accounts.csv"
    source.write_text("id,text\na,Some source text\n", encoding="utf-8")
    with pytest.raises(InputDataError, match="not found"):
        load_input_items(source, only_ids=tmp_path / "missing.csv")


def test_cli_prog_and_mcp_install_use_harness_name(tmp_path, monkeypatch):
    import harness_fleet.cli as cli_module

    assert cli_module.build_parser().prog == "harness-fleet"
    monkeypatch.setattr("sys.argv", ["harness-fleet", "doctor"])
    assert cli_module._package_version()

    config = tmp_path / "mcp.json"
    monkeypatch.setattr("harness_fleet.cli._existing_mcp_path_for_client", lambda *a: config)
    (tmp_path / "harness-fleet.db").touch()
    args = build_parser().parse_args(
        ["mcp", "install", "--client", "cursor", "--workspace-root", str(tmp_path),
         "--db", "harness-fleet.db", "--json"]
    )
    cmd_mcp_install(args)
    installed = json.loads(config.read_text(encoding="utf-8"))
    assert "harness-fleet" in installed["mcpServers"]
    assert "free-fleet" not in installed["mcpServers"]


def test_mcp_install_preserves_invalid_existing_config(tmp_path, monkeypatch):
    config = tmp_path / "mcp.json"
    config.write_text("{broken config", encoding="utf-8")
    monkeypatch.setattr("harness_fleet.cli._existing_mcp_path_for_client", lambda *a: config)
    args = build_parser().parse_args(["mcp", "install", "--client", "cursor", "--workspace-root", str(tmp_path), "--json"])
    with pytest.raises(ValueError, match="config"):
        cmd_mcp_install(args)
    assert config.read_text(encoding="utf-8") == "{broken config"


def test_csv_survivor_ids_match_the_same_normalized_ids(tmp_path):
    source = tmp_path / "accounts.csv"
    source.write_text("id,text\nhttps://example.com,Some source text\n", encoding="utf-8")
    assert len(load_input_items(source, only_ids={"https://example.com"})) == 1


def test_doctor_uses_selected_workspace_and_accepts_local_provider(tmp_path, monkeypatch, capsys):
    from harness_fleet.cli import cmd_doctor
    monkeypatch.setattr("shutil.which", lambda name: None)
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.delenv("OLLAMA_BASE_URL", raising=False)
    monkeypatch.delenv("HARNESS_FLEET_DB", raising=False)
    monkeypatch.delenv("HARNESS_FLEET_DB", raising=False)
    catalog = RouteCatalog(db_path=tmp_path / "harness-fleet.db")
    catalog.add_route("ollama/model", "ollama", 0, 0)
    cmd_doctor(build_parser().parse_args(["doctor", "--workspace-root", str(tmp_path), "--json"]))
    result = json.loads(capsys.readouterr().out)
    assert result["ready"]
    assert Path(result["database"]) == (tmp_path / "harness-fleet.db").resolve()


@pytest.mark.parametrize("platform,subpath", [("win32", "AppData/Roaming/Claude"), ("linux", ".config/Claude"), ("darwin", "Library/Application Support/Claude")])
def test_client_config_default_matches_operating_system(tmp_path, monkeypatch, platform, subpath):
    from harness_fleet.cli import _claude_config_candidates
    monkeypatch.setattr("harness_fleet.cli.sys.platform", platform)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.delenv("APPDATA", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    assert _claude_config_candidates()[0] == tmp_path / subpath / "claude_desktop_config.json"


@pytest.mark.parametrize("name", ["openrouter", "openai_compatible"])
def test_different_worker_timeouts_do_not_close_an_active_http_client(monkeypatch, name):
    import importlib
    module = importlib.import_module("harness_fleet.providers." + name)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setattr(module, "_shared_client", None)
    first = module._shared_httpx_client(5)
    try:
        assert module._shared_httpx_client(30) is first
        assert not first.is_closed
    finally:
        first.close()
