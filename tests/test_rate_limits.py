import json

from harness_fleet.catalog import RouteCatalog
from harness_fleet.engine import Engine
from harness_fleet.models import TaskSpec
from harness_fleet.packer import pack_items
from harness_fleet.store import HarnessStore


class FlakyRateLimitProvider:
    def __init__(self, fail_routes, success_payload):
        self.fail_routes = set(fail_routes)
        self.success_payload = success_payload
        self.calls = []

    def run_prompt(self, route_id, prompt, system_prompt=None, timeout_sec=120, session_id=None, **kwargs):
        self.calls.append(route_id)
        if route_id in self.fail_routes:
            return False, None, {
                "id": f"rec-{len(self.calls)}",
                "session_id": session_id,
                "provider": "mock",
                "requested_route": route_id,
                "status": "failed",
                "cost": None,
                "cost_status": "unknown",
                "usage": None,
                "error": "Rate limited (429)",
                "error_type": "rate_limit",
                "retry_after": 20.0,
                "duration_seconds": 0.1,
            }
        return True, json.dumps(self.success_payload), {
            "id": f"rec-{len(self.calls)}",
            "session_id": session_id,
            "provider": "mock",
            "requested_route": route_id,
            "status": "complete",
            "cost": 0.0,
            "cost_status": "reported_zero",
            "usage": {"total_tokens": 10},
            "error": None,
            "duration_seconds": 0.2,
        }


def test_rate_limit_cools_down_and_fails_over_non_destructively(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    catalog = RouteCatalog(config_path=tmp_path / "routes.json", db_path=tmp_path / "test.db")
    catalog.data = {
        "revision": 2,
        "routes": [
            {"id": "route/busy", "provider": "mock", "enabled": True, "price_state": "price_observed_zero"},
            {"id": "route/free-backup", "provider": "mock", "enabled": True, "price_state": "price_observed_zero"},
        ],
    }
    catalog.save()

    # Prioritize route/busy so it is tested first
    store.record_route_eval({
        "task_name": "classify",
        "route_id": "route/busy",
        "provider": "mock",
        "total_samples": 10,
        "schema_pass_count": 10,
        "grounding_pass_count": 10,
        "error_count": 0,
        "rate_limit_count": 0,
        "avg_latency_seconds": 0.5,
        "composite_score": 0.95,
    })
    store.record_route_eval({
        "task_name": "classify",
        "route_id": "route/free-backup",
        "provider": "mock",
        "total_samples": 10,
        "schema_pass_count": 10,
        "grounding_pass_count": 10,
        "error_count": 0,
        "rate_limit_count": 0,
        "avg_latency_seconds": 0.5,
        "composite_score": 0.80,
    })

    task = TaskSpec(
        name="classify",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )

    payload = {
        "items": [{
            "item_id": "item1",
            "claims": {"summary": "valid"},
            "quotes": [{"slice_id": "full", "start": 0, "end": 15, "text": "target text row"}],
        }]
    }

    mock_prov = FlakyRateLimitProvider(fail_routes=["route/busy"], success_payload=payload)
    engine = Engine(task=task, catalog=catalog, store=store)
    engine.registry.register("mock", mock_prov)

    batch = pack_items([{"item_id": "item1", "text": "target text row"}], batch_size=2)[0]
    ok, results, receipt, error = engine.execute_batch(batch)

    assert ok is True
    assert results is not None
    assert len(results) == 1
    assert mock_prov.calls == ["route/busy", "route/free-backup"]
    # Verify that route/busy was placed in cooldown
    assert catalog.is_cooled_down("route/busy") is True
    assert catalog.is_cooled_down("route/free-backup") is False
