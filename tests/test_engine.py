import json

from free_fleet.catalog import RouteCatalog
from free_fleet.engine import Engine
from free_fleet.models import RoutePolicy, TaskSpec
from free_fleet.packer import pack_items
from free_fleet.profile import IdealCompanyProfile
from free_fleet.store import FreeFleetStore


class ProviderStub:
    def __init__(self, payload):
        self.payload = payload
        self.calls = 0

    def run_prompt(self, route_id, prompt, system_prompt=None, timeout_sec=120, session_id=None):
        self.calls += 1
        self.last_prompt = prompt
        receipt = {
            "id": "receipt-1",
            "session_id": session_id,
            "provider": "stub",
            "requested_route": route_id,
            "status": "complete",
            "cost": 0.0,
            "cost_status": "reported_zero",
            "usage": {"total_tokens": 1},
            "error": None,
            "duration_seconds": 0.01,
        }
        return True, json.dumps(self.payload), receipt


def _engine(tmp_path, payload):
    catalog = RouteCatalog(config_path=tmp_path / "routes.json")
    catalog.data = {
        "revision": 2,
        "routes": [{"id": "stub/zero", "provider": "stub", "enabled": True, "price_state": "price_observed_zero"}],
    }
    task = TaskSpec(
        name="typed",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )
    engine = Engine(task, catalog=catalog)
    engine.opencode_prov = ProviderStub(payload)
    return engine


def test_engine_rejects_extra_model_fields(tmp_path):
    payload = {
        "items": [{
            "item_id": "i1",
            "claims": {"summary": "supported"},
            "quotes": [{"slice_id": "full", "start": 0, "end": 21, "text": "supported source text"}],
            "untrusted": "leak",
        }]
    }
    ok, _, _, error = _engine(tmp_path, payload).execute_batch(pack_items([{"item_id": "i1", "text": "supported source text"}])[0])
    assert ok is False
    assert "Typed output validation failed" in error


def test_engine_exports_only_validated_shape(tmp_path):
    payload = {
        "items": [{
            "item_id": "i1",
            "claims": {"summary": "supported"},
            "quotes": [{"slice_id": "full", "start": 0, "end": 21, "text": "supported source text"}],
        }]
    }
    ok, results, _, error = _engine(tmp_path, payload).execute_batch(pack_items([{"item_id": "i1", "text": "supported source text"}])[0])
    assert ok is True
    assert error is None
    assert results[0]["item_id"] == "i1"
    assert results[0]["claims"] == payload["items"][0]["claims"]
    assert results[0]["quotes"] == payload["items"][0]["quotes"]
    assert results[0]["content_type"] == "text/plain"
    assert len(results[0]["source_digest"]) == 64


def test_campaign_state_and_receipts_live_in_sqlite(tmp_path):
    payload = {
        "items": [{
            "item_id": "i1",
            "claims": {"summary": "supported"},
            "quotes": [{"slice_id": "full", "text": "supported source text"}],
        }]
    }
    engine = _engine(tmp_path, payload)
    output = tmp_path / "packet.json"
    packet = engine.run_campaign(
        raw_items=[{"item_id": "i1", "text": "supported source text"}],
        run_id="run-1",
        input_path="input.jsonl",
        concurrency=2,
        max_attempts=3,
        output_packet_path=output,
    )

    assert packet["total_verified_records"] == 1
    assert packet["$schema"].endswith("/packet-v2.schema.json")
    assert packet["task"]["name"] == "typed"
    assert packet["audit"]["model_attempts"] == 1
    assert len(packet["receipts"]) == 1
    assert engine.store.run_snapshot("run-1")["status"] == "completed"
    assert engine.store.model_run_count("run-1") == 1

    calls = engine.opencode_prov.calls
    engine.resume_campaign("run-1", concurrency=2, output_packet_path=output)
    assert engine.opencode_prov.calls == calls


def test_profile_context_is_used_and_revision_survives_resume(tmp_path):
    engine = _engine(tmp_path, {
        "items": [{
            "item_id": "i1",
            "claims": {"summary": "supported"},
            "quotes": [{"slice_id": "full", "text": "supported source text"}],
        }]
    })
    profile = IdealCompanyProfile(
        profile_name="Database Buyers",
        required_stack=["PostgreSQL"],
    )
    output = tmp_path / "profile-packet.json"
    packet = engine.run_campaign(
        raw_items=[{"item_id": "i1", "text": "supported source text"}],
        run_id="profile-context-run",
        input_path="input.jsonl",
        max_attempts=3,
        output_packet_path=output,
        profile=profile,
    )

    revision = engine.store.active_profile_revision_id()
    assert revision
    assert engine.store.run_snapshot("profile-context-run")["profile_revision_id"] == revision
    assert "IDEAL COMPANY PROFILE: Database Buyers" in engine.opencode_prov.last_prompt
    assert packet["total_verified_records"] == 1
    engine.resume_campaign("profile-context-run", output_packet_path=output)
    assert engine.profile.profile_name == "Database Buyers"


def test_inference_attempts_recorded_in_sqlite_on_failure_and_success(tmp_path):
    class MultiRouteStub:
        def __init__(self):
            self.calls = []

        def run_prompt(self, route_id, prompt, system_prompt=None, timeout_sec=120, session_id=None, policy=None):
            self.calls.append(route_id)
            receipt = {
                "id": f"rec-{len(self.calls)}",
                "session_id": session_id,
                "provider": "stub",
                "requested_route": route_id,
                "status": "complete",
                "cost": 0.0,
                "cost_status": "reported_zero",
                "usage": {"total_tokens": 10},
                "error": None,
                "duration_seconds": 0.05,
            }
            if len(self.calls) == 1:
                # Returns malformed JSON on first attempt
                return True, "not valid json here", receipt
            else:
                # Returns valid verified result on second attempt
                valid_payload = {
                    "items": [{
                        "item_id": "i1",
                        "claims": {"summary": "supported"},
                        "quotes": [{"slice_id": "full", "text": "supported source text"}],
                    }]
                }
                return True, json.dumps(valid_payload), receipt

    catalog = RouteCatalog(db_path=tmp_path / "state.db")
    catalog.add_route("stub/fail", provider="stub", cost_per_1k_input=0.0, cost_per_1k_output=0.0, price_state="price_observed_zero")
    catalog.add_route("stub/pass", provider="stub", cost_per_1k_input=0.0, cost_per_1k_output=0.0, price_state="price_observed_zero")

    task = TaskSpec(
        name="multi-test",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )
    engine = Engine(task, catalog=catalog)
    stub_provider = MultiRouteStub()
    engine.registry.register("stub", stub_provider)

    batch = pack_items([{"item_id": "i1", "text": "supported source text"}])[0]
    ok, results, receipt, error = engine.execute_batch(batch, run_id="run-telemetry")

    assert ok is True
    assert len(results) == 1

    # Verify inference_attempts ledger in store
    with engine.store.connect() as conn:
        attempts = conn.execute(
            "SELECT route_id, transport_status, parse_status, outcome, verified FROM inference_attempts WHERE run_id='run-telemetry' ORDER BY started_at ASC"
        ).fetchall()

    assert len(attempts) == 2
    first_route = stub_provider.calls[0]
    second_route = stub_provider.calls[1]

    assert attempts[0]["route_id"] == first_route
    assert attempts[0]["parse_status"] == "malformed_json"
    assert attempts[0]["outcome"] == "parse_failed"
    assert attempts[0]["verified"] == 0

    assert attempts[1]["route_id"] == second_route
    assert attempts[1]["parse_status"] == "success"
    assert attempts[1]["outcome"] == "verified"
    assert attempts[1]["verified"] == 1

    # Verify route history stats reflects the attempts (decayed effective counts ≈1)
    stats = engine.store.get_route_history_stats(task_name="multi-test")
    assert first_route in stats
    # Totals are decayed effective sample sizes (floats), not raw counts.
    assert abs(stats[first_route]["total"] - 1.0) < 1e-3
    assert abs(stats[first_route]["completed"] - 0.0) < 1e-3
    assert abs(stats[first_route]["malformed"] - 1.0) < 1e-3

    assert second_route in stats
    assert abs(stats[second_route]["total"] - 1.0) < 1e-3
    assert abs(stats[second_route]["completed"] - 1.0) < 1e-3


def test_new_resume_session_does_not_reactivate_stored_paid_approval(tmp_path):
    store = FreeFleetStore(tmp_path / "state.db")
    catalog = RouteCatalog(db_path=store.path)
    catalog.add_route(
        "paid/model",
        provider="stub",
        cost_per_1k_input=1.0,
        cost_per_1k_output=2.0,
        enabled=True,
        price_state="unknown",
    )
    task = TaskSpec(
        name="paid-resume",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )
    revision = store.register_task(task)
    store.create_run(
        run_id="paid-resume-run",
        task_revision_id=revision,
        input_path="input.jsonl",
        input_digest="a" * 64,
        total_items=1,
        max_attempts=3,
        batch_size=1,
        output_path=str(tmp_path / "packet.json"),
        policy=RoutePolicy(allowed_routes=["paid/model"]),
    )
    store.enqueue_batches(
        "paid-resume-run",
        pack_items([{"item_id": "i1", "text": "supported source text"}]),
        max_attempts_per_batch=3,
    )

    fresh_engine = Engine(task=task, store=store, catalog=catalog)
    try:
        fresh_engine.resume_campaign("paid-resume-run", concurrency=1)
    except RuntimeError as exc:
        assert "previously approved paid route" in str(exc)
        assert "explicit --route approval" in str(exc)
    else:
        raise AssertionError("a stored paid approval was reused by a new session")
