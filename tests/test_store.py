import sqlite3
from concurrent.futures import ThreadPoolExecutor

import pytest

from free_fleet.models import ProviderReceipt, RouteInfo, TaskSpec
from free_fleet.packer import pack_items
from free_fleet.profile import IdealCompanyProfile
from free_fleet.store import BulkLanesStore, digest_json


def _task():
    return TaskSpec(name="demo")


def _run(store, run_id="run-1", count=1):
    revision = store.register_task(_task())
    records = [{"item_id": f"i{i}", "text": f"source text number {i}"} for i in range(count)]
    batches = pack_items(records, batch_size=1)
    store.create_run(run_id, revision, "input.jsonl", digest_json(records), count, 20, 1, "packet.json")
    store.enqueue_batches(run_id, batches, 3)
    return batches


def test_schema_has_recovered_control_plane_tables(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    with store.connect() as connection:
        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    assert {
        "task_revisions", "current_tasks", "route_observations", "current_routes",
        "runs", "batches", "batch_attempts", "model_runs", "batch_results", "current_batch_results", "worker_sessions",
        "route_cooldowns", "route_evaluations", "inference_attempts", "profile_revisions", "active_profiles",
    } <= tables
    assert store.schema_version() == "4"
    with store.connect() as connection:
        task_columns = {row[1] for row in connection.execute("PRAGMA table_info(task_revisions)")}
    assert "spec_json" not in task_columns
    assert {"format_version", "instructions", "batch_size", "max_slice_chars", "min_quote_chars", "claims_schema_json"} <= task_columns


def test_ideal_company_profile_revisions_round_trip_and_activate(tmp_path):
    store = BulkLanesStore(tmp_path / "profiles.db")
    first = IdealCompanyProfile(profile_name="First ICP", required_stack=["PostgreSQL"])
    second = IdealCompanyProfile(profile_name="Second ICP", required_stack=["Kafka"])

    first_revision = store.save_profile(first)
    assert store.active_profile_revision_id() == first_revision
    assert store.load_profile().model_dump() == first.model_dump()

    second_revision = store.save_profile(second)
    assert second_revision != first_revision
    assert store.active_profile_revision_id() == second_revision
    assert store.load_profile().model_dump() == second.model_dump()
    with store.connect() as connection:
        assert connection.execute("SELECT count(*) FROM profile_revisions WHERE profile_kind='ideal_company'").fetchone()[0] == 2


def test_legacy_database_migrates_profile_tables_and_run_reference(tmp_path):
    db_path = tmp_path / "legacy.db"
    with sqlite3.connect(db_path) as connection:
        connection.executescript(
            """
            CREATE TABLE bulk_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
            INSERT INTO bulk_meta(key, value) VALUES ('schema_version', '2');
            CREATE TABLE runs (
                run_id TEXT PRIMARY KEY,
                task_revision_id TEXT NOT NULL,
                input_path TEXT NOT NULL,
                input_digest TEXT NOT NULL,
                status TEXT NOT NULL,
                total_items INTEGER NOT NULL,
                max_attempts INTEGER NOT NULL,
                attempts_used INTEGER NOT NULL DEFAULT 0,
                batch_size INTEGER NOT NULL,
                output_path TEXT,
                created_at TEXT NOT NULL,
                finished_at TEXT
            );
            """
        )

    store = BulkLanesStore(db_path)
    with store.connect() as connection:
        run_columns = {row[1] for row in connection.execute("PRAGMA table_info(runs)")}
        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    assert {"policy_json", "profile_revision_id"} <= run_columns
    assert {"profile_revisions", "active_profiles"} <= tables
    assert store.schema_version() == "4"


def test_run_snapshot_records_selected_profile_revision(tmp_path):
    store = BulkLanesStore(tmp_path / "run-profile.db")
    profile_revision = store.save_profile(IdealCompanyProfile(profile_name="Audited ICP"))
    task_revision = store.register_task(_task())
    store.create_run(
        run_id="profile-run",
        task_revision_id=task_revision,
        input_path="input.jsonl",
        input_digest="a" * 64,
        total_items=0,
        max_attempts=1,
        batch_size=1,
        output_path="output.json",
        profile_revision_id=profile_revision,
    )

    assert store.run_snapshot("profile-run")["profile_revision_id"] == profile_revision


def test_run_rejects_unknown_profile_revision(tmp_path):
    store = BulkLanesStore(tmp_path / "missing-profile.db")
    task_revision = store.register_task(_task())
    with pytest.raises(ValueError, match="profile revision does not exist"):
        store.create_run(
            run_id="missing-profile-run",
            task_revision_id=task_revision,
            input_path="input.jsonl",
            input_digest="a" * 64,
            total_items=0,
            max_attempts=1,
            batch_size=1,
            output_path="output.json",
            profile_revision_id="missing-revision",
        )


def test_concurrent_lease_claims_batch_once(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    _run(store)
    with ThreadPoolExecutor(max_workers=8) as pool:
        leases = list(pool.map(lambda i: store.lease_batch("run-1", f"worker-{i}"), range(8)))
    assert sum(lease is not None for lease in leases) == 1
    assert store.run_snapshot("run-1")["attempts_used"] == 1


def test_twenty_concurrent_leases_are_unique(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    _run(store, count=20)
    with ThreadPoolExecutor(max_workers=20) as pool:
        leases = list(pool.map(lambda i: store.lease_batch("run-1", f"worker-{i}"), range(20)))
    assert all(lease is not None for lease in leases)
    assert len({lease["batch"]["batch_id"] for lease in leases}) == 20
    assert store.run_snapshot("run-1")["attempts_used"] == 20


def test_task_revisions_and_model_receipts_are_append_only(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    batches = _run(store)
    lease = store.lease_batch("run-1", "worker")
    receipt = ProviderReceipt(
        id="receipt-1", provider="fixture", requested_route="fixture/zero", status="complete",
        cost=0.0, cost_status="reported_zero", usage={"total_tokens": 1}, duration_seconds=0.01,
    )
    packed_item = batches[0]["items"][0]
    result = [{
        "item_id": "i0", "source_uri": None, "source_digest": packed_item["source_digest"],
        "content_type": "text/plain", "claims": {"summary": "ok", "category": "test"},
        "quotes": [{"slice_id": "full", "start": 0, "end": 20, "text": "source text number 0"}],
    }]
    store.complete_batch("run-1", lease["attempt_id"], "worker", result, receipt)
    with store.connect() as connection:
        with pytest.raises(Exception, match="append-only"):
            connection.execute("DELETE FROM model_runs")
        with pytest.raises(Exception, match="append-only"):
            connection.execute("UPDATE task_revisions SET task_name='changed'")
        with pytest.raises(Exception, match="append-only"):
            connection.execute("DELETE FROM batch_results")


def test_failed_attempt_requeues_until_batch_limit(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    _run(store)
    for number in range(1, 4):
        lease = store.lease_batch("run-1", f"worker-{number}")
        assert lease["attempt_number"] == number
        store.fail_batch("run-1", lease["attempt_id"], f"worker-{number}", "bad output", None)
    assert store.lease_batch("run-1", "last") is None
    assert store.run_snapshot("run-1")["batches"][next(iter(store.run_snapshot("run-1")["batches"]))]["status"] == "failed"


def test_route_observations_are_versioned_and_current_is_explicit(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    store.upsert_route(RouteInfo(id="provider/model", provider="provider", enabled=False, price_state="candidate"))
    store.upsert_route(RouteInfo(
        id="provider/model", provider="provider", enabled=True, price_state="price_observed_zero",
        cost_per_1k_input=0.0, cost_per_1k_output=0.0,
    ))
    with store.connect() as connection:
        assert connection.execute("SELECT count(*) FROM route_observations").fetchone()[0] == 2
        current = connection.execute(
            "SELECT o.route_json FROM current_routes c JOIN route_observations o ON o.observation_id=c.observation_id"
        ).fetchone()[0]
        assert __import__("json").loads(current)["price_state"] == "price_observed_zero"
        with pytest.raises(Exception, match="append-only"):
            connection.execute("DELETE FROM route_observations")


def test_identical_route_observations_retain_each_event(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    route = RouteInfo(id="provider/model", provider="provider", enabled=False, price_state="candidate")
    store.upsert_route(route)
    store.upsert_route(route)
    with store.connect() as connection:
        assert connection.execute("SELECT count(*) FROM route_observations").fetchone()[0] == 2


def test_run_identifier_cannot_escape_output_shape(tmp_path):
    store = BulkLanesStore(tmp_path / "state.db")
    revision = store.register_task(_task())
    with pytest.raises(ValueError, match="run_id"):
        store.create_run("../escape", revision, "input", "digest", 1, 1, 1, "output")
