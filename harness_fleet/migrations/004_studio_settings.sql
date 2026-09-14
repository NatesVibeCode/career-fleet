-- Studio settings: the harness/model selection picked in the local studio UI.
-- A single active row; the revision id is a content digest so a run can be
-- audited against the exact selection that produced it.
CREATE TABLE IF NOT EXISTS studio_settings (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    mode TEXT NOT NULL CHECK(mode IN ('free','specific')),
    providers_json TEXT NOT NULL,
    routes_json TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
