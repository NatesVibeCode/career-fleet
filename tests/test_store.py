import sqlite3
from concurrent.futures import ThreadPoolExecutor

import pytest

from harness_fleet.models import ProviderReceipt, RouteInfo, TaskSpec
from harness_fleet.packer import pack_items
from harness_fleet.profile import IdealCompanyProfile
from harness_fleet.store import HarnessStore, digest_json


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
    store = HarnessStore(tmp_path / "state.db")
    with store.connect() as connection:
        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    assert {
        "task_revisions", "current_tasks", "route_observations", "current_routes",
        "runs", "batches", "batch_attempts", "model_runs", "batch_results", "current_batch_results", "worker_sessions",
        "route_cooldowns", "route_evaluations", "inference_attempts", "profile_revisions", "active_profiles",
    } <= tables
    assert store.schema_version() == "5"
    with store.connect() as connection:
        task_columns = {row[1] for row in connection.execute("PRAGMA table_info(task_revisions)")}
    assert "spec_json" not in task_columns
    assert {"format_version", "instructions", "batch_size", "max_slice_chars", "min_quote_chars", "claims_schema_json"} <= task_columns


def test_ideal_company_profile_revisions_round_trip_and_activate(tmp_path):
    store = HarnessStore(tmp_path / "profiles.db")
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
    # NOTE: sqlite3 connections do not close on `with` exit (the block only
    # commits); close explicitly so Windows tmp cleanup never hits a lock.
    seed = sqlite3.connect(db_path)
    try:
        seed.executescript(
            """
            CREATE TABLE harness_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
            INSERT INTO harness_meta(key, value) VALUES ('schema_version', '2');
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
    finally:
        seed.close()

    store = HarnessStore(db_path)
    with store.connect() as connection:
        run_columns = {row[1] for row in connection.execute("PRAGMA table_info(runs)")}
        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    assert {"policy_json", "profile_revision_id"} <= run_columns
    assert {"profile_revisions", "active_profiles"} <= tables
    assert store.schema_version() == "5"


def test_run_snapshot_records_selected_profile_revision(tmp_path):
    store = HarnessStore(tmp_path / "run-profile.db")
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
    store = HarnessStore(tmp_path / "missing-profile.db")
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
    store = HarnessStore(tmp_path / "state.db")
    _run(store)
    with ThreadPoolExecutor(max_workers=8) as pool:
        leases = list(pool.map(lambda i: store.lease_batch("run-1", f"worker-{i}"), range(8)))
    assert sum(lease is not None for lease in leases) == 1
    assert store.run_snapshot("run-1")["attempts_used"] == 1


def test_twenty_concurrent_leases_are_unique(tmp_path):
    store = HarnessStore(tmp_path / "state.db")
    _run(store, count=20)
    with ThreadPoolExecutor(max_workers=20) as pool:
        leases = list(pool.map(lambda i: store.lease_batch("run-1", f"worker-{i}"), range(20)))
    assert all(lease is not None for lease in leases)
    assert len({lease["batch"]["batch_id"] for lease in leases}) == 20
    assert store.run_snapshot("run-1")["attempts_used"] == 20


def test_task_revisions_and_model_receipts_are_append_only(tmp_path):
    store = HarnessStore(tmp_path / "state.db")
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
    store = HarnessStore(tmp_path / "state.db")
    _run(store)
    for number in range(1, 4):
        lease = store.lease_batch("run-1", f"worker-{number}")
        assert lease["attempt_number"] == number
        store.fail_batch("run-1", lease["attempt_id"], f"worker-{number}", "bad output", None)
    assert store.lease_batch("run-1", "last") is None
    assert store.run_snapshot("run-1")["batches"][next(iter(store.run_snapshot("run-1")["batches"]))]["status"] == "failed"


def test_route_observations_are_versioned_and_current_is_explicit(tmp_path):
    store = HarnessStore(tmp_path / "state.db")
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
    store = HarnessStore(tmp_path / "state.db")
    route = RouteInfo(id="provider/model", provider="provider", enabled=False, price_state="candidate")
    store.upsert_route(route)
    store.upsert_route(route)
    with store.connect() as connection:
        assert connection.execute("SELECT count(*) FROM route_observations").fetchone()[0] == 2


def test_run_identifier_cannot_escape_output_shape(tmp_path):
    store = HarnessStore(tmp_path / "state.db")
    revision = store.register_task(_task())
    with pytest.raises(ValueError, match="run_id"):
        store.create_run("../escape", revision, "input", "digest", 1, 1, 1, "output")

def test_legacy_database_migrates_to_harness_values(tmp_path):
    """R21 upgrade: old-shape DBs auto-migrate on first open with no data loss."""
    import sqlite3

    db_path = tmp_path / "legacy.db"
    connection = sqlite3.connect(str(db_path))
    connection.execute(
        "CREATE TABLE bulk_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)"
    )
    connection.execute(
        "INSERT INTO bulk_meta(key,value) VALUES('schema_version','4')"
    )
    connection.execute(
        """CREATE TABLE task_revisions(
            revision_id TEXT PRIMARY KEY,
            task_name TEXT NOT NULL,
            format_version TEXT NOT NULL CHECK(format_version IN ('free_fleet_task_v1', 'bulk_lanes_task_v1')),
            instructions TEXT NOT NULL,
            batch_size INTEGER NOT NULL CHECK(batch_size > 0),
            max_slice_chars INTEGER NOT NULL CHECK(max_slice_chars >= 300),
            min_quote_chars INTEGER NOT NULL CHECK(min_quote_chars > 0),
            claims_schema_json TEXT NOT NULL,
            created_at TEXT NOT NULL
        )"""
    )
    rows = [
        ("a" * 64, "legacy-a", "free_fleet_task_v1"),
        ("b" * 64, "legacy-b", "bulk_lanes_task_v1"),
    ]
    for revision_id, name, version in rows:
        connection.execute(
            "INSERT INTO task_revisions(revision_id,task_name,format_version,instructions,"
            "batch_size,max_slice_chars,min_quote_chars,claims_schema_json,created_at)"
            " VALUES(?,?,?,?,?,?,?,?,?)",
            (revision_id, name, version, "Do things.", 6, 6000, 15,
             '{"type":"object","additionalProperties":false}', "2026-01-01T00:00:00+00:00"),
        )
    connection.execute(
        "CREATE TABLE current_tasks(task_name TEXT PRIMARY KEY, revision_id TEXT NOT NULL)"
    )
    connection.execute(
        "INSERT INTO current_tasks(task_name,revision_id) VALUES('legacy-a',?)", ("a" * 64,)
    )
    connection.commit()
    connection.close()

    store = HarnessStore(db_path)
    with store.connect() as check:
        tables = {row[0] for row in check.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        assert "harness_meta" in tables
        assert "bulk_meta" not in tables
        assert store.schema_version() == "5"
        triggers = {row[0] for row in check.execute("SELECT name FROM sqlite_master WHERE type='trigger'")}
        assert {"task_revisions_no_update", "task_revisions_no_delete"} <= triggers
        assert check.execute("SELECT count(*) FROM task_revisions").fetchone()[0] == 2
        assert check.execute("SELECT count(*) FROM current_tasks").fetchone()[0] == 1
        versions = {
            row[0] for row in check.execute("SELECT DISTINCT format_version FROM task_revisions")
        }
        assert versions == {"harness_fleet_task_v1"}
        check_sql = check.execute(
            "SELECT sql FROM sqlite_master WHERE type='table' AND name='task_revisions'"
        ).fetchone()[0]
        assert "harness_fleet_task_v1" in check_sql
        assert "free_fleet_task_v1" not in check_sql
        assert "bulk_lanes_task_v1" not in check_sql
    # The migrated rows remain readable through the new store.
    assert store.get_task("legacy-a").format_version == "harness_fleet_task_v1"
    # Re-open is idempotent: counts and values are stable.
    reopened = HarnessStore(db_path)
    with reopened.connect() as check:
        assert check.execute("SELECT count(*) FROM task_revisions").fetchone()[0] == 2
        assert {
            row[0] for row in check.execute("SELECT DISTINCT format_version FROM task_revisions")
        } == {"harness_fleet_task_v1"}


def test_legacy_database_with_both_meta_tables_migrates(tmp_path):
    """Both-tables branch: a fresh harness_meta plus legacy bulk_meta merges."""

    db_path = tmp_path / "both.db"
    store = HarnessStore(db_path)
    with store.connect() as check:
        check.execute("CREATE TABLE bulk_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        check.execute("INSERT INTO bulk_meta(key,value) VALUES('schema_version','4')")
    reopened = HarnessStore(db_path)
    with reopened.connect() as check:
        tables = {row[0] for row in check.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        assert "harness_meta" in tables
        assert "bulk_meta" not in tables
        assert reopened.schema_version() == "5"


def test_advertised_schema_matches_the_database_migrate_builds(tmp_path):
    """Introspecting agents must see every table/index that actually exists.

    score_history and route_claim_bias were created by inline DDL in migrate()
    and so were missing from get_database_schema_sql() -- the schema the CLI and
    MCP tools report.
    """
    import re
    import sqlite3

    from harness_fleet.store import get_database_schema_sql

    db = tmp_path / "schema.db"
    HarnessStore(db)
    connection = sqlite3.connect(db)
    try:
        actual_tables = sorted(
            row[0] for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        )
        actual_indexes = sorted(
            row[0] for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'"
            )
        )
    finally:
        connection.close()

    advertised = get_database_schema_sql()
    advertised_tables = sorted({
        line.split()[5].strip('("')
        for line in advertised.splitlines()
        if line.upper().startswith("CREATE TABLE")
    })
    advertised_indexes = sorted(set(
        re.findall(r"CREATE INDEX IF NOT EXISTS\s+(\w+)", advertised, re.IGNORECASE)
    ))

    assert advertised_tables == actual_tables
    assert advertised_indexes == actual_indexes
    assert {"score_history", "route_claim_bias", "studio_settings"} <= set(actual_tables)


def test_migrate_is_idempotent_and_upgrades_a_pre_studio_database(tmp_path):
    """An existing database must gain the new tables in place, keeping its rows."""
    import sqlite3

    from harness_fleet.store import (
        MIGRATION_002_PATH,
        MIGRATION_003_PATH,
        SCHEMA_SQL,
        SCHEMA_VERSION,
    )

    db = tmp_path / "legacy.db"
    connection = sqlite3.connect(db)
    try:
        connection.executescript(SCHEMA_SQL)
        connection.executescript(MIGRATION_002_PATH.read_text(encoding="utf-8"))
        connection.executescript(MIGRATION_003_PATH.read_text(encoding="utf-8"))
        connection.execute(
            "INSERT INTO harness_meta(key,value) VALUES('schema_version','4') "
            "ON CONFLICT(key) DO UPDATE SET value='4'"
        )
        connection.commit()
        assert "studio_settings" not in {
            row[0] for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type='table'"
            )
        }
    finally:
        connection.close()

    store = HarnessStore(db)
    assert store.schema_version() == SCHEMA_VERSION

    connection = sqlite3.connect(db)
    try:
        tables = {row[0] for row in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table'"
        )}
        assert {"studio_settings", "score_history", "route_claim_bias"} <= tables
    finally:
        connection.close()

    # Re-running migrate() must not disturb an already-current database.
    store.migrate()
    store.migrate()
    assert store.schema_version() == SCHEMA_VERSION
