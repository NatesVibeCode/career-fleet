import json

from harness_fleet.cli import main
from harness_fleet.engine import Engine
from harness_fleet.models import TaskSpec
from harness_fleet.store import HarnessStore


class MockProvider:
    def run_prompt(self, route_id, prompt, system_prompt=None, timeout_sec=120, session_id=None, **kwargs):
        payload = {
            "items": [{
                "item_id": "i1",
                "claims": {"summary": "sample text"},
                "quotes": [{"slice_id": "full", "start": 0, "end": 11, "text": "sample text"}],
            }]
        }
        return True, json.dumps(payload), {
            "id": "rec-1",
            "session_id": session_id,
            "provider": "mock",
            "requested_route": route_id,
            "status": "complete",
            "cost": 0.0,
            "cost_status": "reported_zero",
            "usage": {"total_tokens": 10},
            "duration_seconds": 0.5,
        }


def test_status_report_and_cli(tmp_path, monkeypatch, capsys):
    db_path = tmp_path / "test.db"
    store = HarnessStore(db_path)

    from harness_fleet.catalog import RouteCatalog
    catalog = RouteCatalog(config_path=tmp_path / "routes.json", db_path=db_path)
    catalog.data = {
        "revision": 2,
        "routes": [{"id": "route/mock-1", "provider": "mock", "enabled": True, "price_state": "price_observed_zero"}],
    }
    catalog.save()

    task = TaskSpec(
        name="status-test",
        min_quote_chars=5,
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )

    engine = Engine(task=task, store=store, catalog=catalog)
    engine.registry.register("mock", MockProvider())

    run_id = "run-status-check"
    engine.run_campaign(
        raw_items=[{"item_id": "i1", "text": "sample text"}],
        run_id=run_id,
        input_path="input.jsonl",
        concurrency=1,
    )

    # Test direct store status report
    status = store.get_run_status(run_id)
    assert status.run_id == run_id
    assert status.status == "completed"
    assert status.total_items == 1
    assert status.verified_items == 1
    assert status.batches.verified == 1
    assert status.batches.total == 1
    assert len(status.routes) == 1
    assert status.routes[0].route_id == "route/mock-1"
    assert status.routes[0].verified == 1

    # Test CLI invocation
    monkeypatch.setattr("sys.argv", ["harness-fleet", "status", run_id, "--db", str(db_path), "--json"])
    main()
    captured = capsys.readouterr()
    cli_out = json.loads(captured.out)
    assert cli_out["run_id"] == run_id
    assert cli_out["status"] == "completed"
    assert cli_out["verified_items"] == 1
