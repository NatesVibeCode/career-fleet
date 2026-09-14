PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS harness_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_revisions (
    revision_id TEXT PRIMARY KEY,
    task_name TEXT NOT NULL,
    format_version TEXT NOT NULL CHECK(format_version IN ('harness_fleet_task_v1')),
    instructions TEXT NOT NULL,
    batch_size INTEGER NOT NULL CHECK(batch_size > 0),
    max_slice_chars INTEGER NOT NULL CHECK(max_slice_chars >= 300),
    min_quote_chars INTEGER NOT NULL CHECK(min_quote_chars > 0),
    claims_schema_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS current_tasks (
    task_name TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES task_revisions(revision_id)
);

CREATE TABLE IF NOT EXISTS route_observations (
    observation_id TEXT PRIMARY KEY,
    route_id TEXT NOT NULL,
    route_json TEXT NOT NULL,
    price_state TEXT NOT NULL CHECK(price_state IN ('candidate','price_observed_zero','unknown','disabled')),
    enabled INTEGER NOT NULL,
    provider TEXT,
    observed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS current_routes (
    route_id TEXT PRIMARY KEY,
    observation_id TEXT NOT NULL REFERENCES route_observations(observation_id)
);

CREATE TABLE IF NOT EXISTS runs (
    run_id TEXT PRIMARY KEY,
    task_revision_id TEXT NOT NULL REFERENCES task_revisions(revision_id),
    profile_revision_id TEXT REFERENCES profile_revisions(revision_id),
    input_path TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('running','completed','completed_with_failures','budget_exhausted')),
    total_items INTEGER NOT NULL CHECK(total_items >= 0),
    max_attempts INTEGER NOT NULL CHECK(max_attempts > 0),
    attempts_used INTEGER NOT NULL DEFAULT 0 CHECK(attempts_used >= 0),
    batch_size INTEGER NOT NULL CHECK(batch_size > 0),
    output_path TEXT,
    created_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE TABLE IF NOT EXISTS batches (
    run_id TEXT NOT NULL REFERENCES runs(run_id),
    batch_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    item_ids_json TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending','leased','verified','failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    max_attempts INTEGER NOT NULL CHECK(max_attempts > 0),
    non_counting_attempts INTEGER NOT NULL DEFAULT 0 CHECK(non_counting_attempts >= 0),
    lease_owner TEXT,
    leased_at TEXT,
    error TEXT,
    completed_at TEXT,
    PRIMARY KEY(run_id, batch_id)
);

CREATE TABLE IF NOT EXISTS batch_attempts (
    attempt_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL,
    worker_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('leased','verified','failed')),
    receipt_id TEXT,
    error TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    FOREIGN KEY(run_id, batch_id) REFERENCES batches(run_id, batch_id)
);

CREATE TABLE IF NOT EXISTS model_runs (
    receipt_id TEXT PRIMARY KEY,
    run_id TEXT,
    batch_id TEXT,
    provider TEXT NOT NULL,
    requested_route TEXT NOT NULL,
    status TEXT NOT NULL,
    cost REAL,
    cost_status TEXT NOT NULL,
    usage_json TEXT,
    error TEXT,
    duration_seconds REAL,
    receipt_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batch_results (
    result_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    result_json TEXT NOT NULL,
    receipt_id TEXT NOT NULL REFERENCES model_runs(receipt_id),
    created_at TEXT NOT NULL,
    FOREIGN KEY(run_id, batch_id) REFERENCES batches(run_id, batch_id)
);

CREATE TABLE IF NOT EXISTS current_batch_results (
    run_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    result_id TEXT NOT NULL REFERENCES batch_results(result_id),
    PRIMARY KEY(run_id, batch_id),
    FOREIGN KEY(run_id, batch_id) REFERENCES batches(run_id, batch_id)
);

CREATE TABLE IF NOT EXISTS worker_sessions (
    run_id TEXT NOT NULL REFERENCES runs(run_id),
    session_id TEXT NOT NULL,
    session_json TEXT NOT NULL,
    PRIMARY KEY(run_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_batches_queue ON batches(run_id, status, position);
CREATE INDEX IF NOT EXISTS idx_attempts_run ON batch_attempts(run_id, batch_id, attempt_number);
CREATE INDEX IF NOT EXISTS idx_model_runs_run ON model_runs(run_id, batch_id);
CREATE INDEX IF NOT EXISTS idx_batch_results_run ON batch_results(run_id, batch_id);

CREATE TRIGGER IF NOT EXISTS task_revisions_no_update
BEFORE UPDATE ON task_revisions BEGIN SELECT RAISE(ABORT, 'task revisions are append-only'); END;
CREATE TRIGGER IF NOT EXISTS task_revisions_no_delete
BEFORE DELETE ON task_revisions BEGIN SELECT RAISE(ABORT, 'task revisions are append-only'); END;
CREATE TRIGGER IF NOT EXISTS model_runs_no_update
BEFORE UPDATE ON model_runs BEGIN SELECT RAISE(ABORT, 'model runs are append-only'); END;
CREATE TRIGGER IF NOT EXISTS model_runs_no_delete
BEFORE DELETE ON model_runs BEGIN SELECT RAISE(ABORT, 'model runs are append-only'); END;
CREATE TRIGGER IF NOT EXISTS batch_results_no_update
BEFORE UPDATE ON batch_results BEGIN SELECT RAISE(ABORT, 'batch results are append-only'); END;
CREATE TRIGGER IF NOT EXISTS batch_results_no_delete
BEFORE DELETE ON batch_results BEGIN SELECT RAISE(ABORT, 'batch results are append-only'); END;
CREATE TRIGGER IF NOT EXISTS route_observations_no_update
BEFORE UPDATE ON route_observations BEGIN SELECT RAISE(ABORT, 'route observations are append-only'); END;
CREATE TRIGGER IF NOT EXISTS route_observations_no_delete
BEFORE DELETE ON route_observations BEGIN SELECT RAISE(ABORT, 'route observations are append-only'); END;
CREATE TRIGGER IF NOT EXISTS batch_attempts_no_delete
BEFORE DELETE ON batch_attempts BEGIN SELECT RAISE(ABORT, 'batch attempts are append-only'); END;
