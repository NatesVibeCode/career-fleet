from free_fleet.catalog import RouteCatalog
from free_fleet.engine import Engine
from free_fleet.models import RoutePolicy, TaskSpec
from free_fleet.packer import pack_items
from free_fleet.store import FreeFleetStore


def _catalog(tmp_path):
    catalog = RouteCatalog(db_path=tmp_path / "routes.db")
    catalog.data = {
        "revision": 1,
        "routes": [
            {
                "id": "free/model",
                "provider": "stub",
                "enabled": True,
                "price_state": "price_observed_zero",
                "cost_per_1k_input": 0.0,
                "cost_per_1k_output": 0.0,
            },
            {
                "id": "paid/model",
                "provider": "stub",
                "enabled": True,
                "price_state": "unknown",
                "cost_per_1k_input": 1.0,
                "cost_per_1k_output": 2.0,
            },
        ],
    }
    catalog.save()
    return catalog


def test_free_lanes_are_the_default(tmp_path):
    catalog = _catalog(tmp_path)

    assert catalog.get_ladder() == ["free/model"]


def test_cost_limits_do_not_approve_paid_lanes(tmp_path):
    catalog = _catalog(tmp_path)
    policy = RoutePolicy(
        max_cost_per_1k_input=2.0,
        max_cost_per_1k_output=3.0,
        max_request_cost=0.10,
    )

    assert catalog.get_ladder(policy=policy) == ["free/model"]


def test_explicit_route_request_allows_a_paid_lane_for_this_session(tmp_path):
    catalog = _catalog(tmp_path)
    policy = RoutePolicy(allowed_routes=["paid/model"])

    assert catalog.get_ladder(policy=policy) == ["paid/model"]


def test_zero_receipt_does_not_reclassify_a_paid_lane(tmp_path):
    catalog = _catalog(tmp_path)

    catalog.record_cost("paid/model", reported_cost=0.0)

    paid = next(route for route in catalog.data["routes"] if route["id"] == "paid/model")
    assert paid["price_state"] == "unknown"


def test_new_resume_session_does_not_reuse_paid_approval(tmp_path):
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
