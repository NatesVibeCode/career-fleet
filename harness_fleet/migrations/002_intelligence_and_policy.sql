CREATE TABLE IF NOT EXISTS route_cooldowns (
    route_id TEXT PRIMARY KEY,
    cooldown_until REAL NOT NULL,
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS route_evaluations (
    eval_id TEXT PRIMARY KEY,
    task_name TEXT NOT NULL,
    route_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    total_samples INTEGER NOT NULL,
    schema_pass_count INTEGER NOT NULL,
    grounding_pass_count INTEGER NOT NULL,
    correct_count INTEGER,
    error_count INTEGER NOT NULL,
    rate_limit_count INTEGER NOT NULL,
    avg_latency_seconds REAL NOT NULL,
    composite_score REAL NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_route_evals_task ON route_evaluations(task_name, route_id);

CREATE TABLE IF NOT EXISTS inference_attempts (
    attempt_id TEXT PRIMARY KEY,
    run_id TEXT,
    batch_id TEXT,
    lease_attempt_number INTEGER,
    route_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    task_name TEXT,
    started_at TEXT NOT NULL,
    duration_seconds REAL,
    transport_status TEXT NOT NULL,
    parse_status TEXT NOT NULL,
    schema_status TEXT NOT NULL,
    grounding_status TEXT NOT NULL,
    outcome TEXT NOT NULL,
    verified INTEGER NOT NULL,
    error_type TEXT,
    error_message TEXT,
    retry_after REAL,
    cost REAL,
    cost_status TEXT,
    usage_json TEXT,
    counts_against_budget INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_inference_attempts_route ON inference_attempts(route_id, task_name);
CREATE INDEX IF NOT EXISTS idx_inference_attempts_run ON inference_attempts(run_id);
