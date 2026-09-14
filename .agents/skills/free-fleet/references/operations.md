# Operations

Read this for CLI or MCP operation.

## Fresh system

```bash
python3 -m pip install .
free-fleet setup --workspace-root "$PWD" --refresh-routes --json
# One-command MCP for Claude/Cursor (writes mcpServers entry, no manual JSON edit)
free-fleet mcp install --workspace-root "$PWD" --dry-run --json
free-fleet mcp install --workspace-root "$PWD"
```

Setup installs the standard skill at `<workspace>/.agents/skills/free-fleet`, initializes SQLite, and returns one stdio MCP definition. Use `--scope user` for `~/.agents/skills`. If a harness requires another discovery directory, pass that parent through `--skill-root`. Setup is idempotent and refuses to overwrite different content unless `--force` is explicit.

The CLI, JSON contracts, SQLite database, and stdio MCP server do not depend on skill discovery. The skill only supplies the exact operating procedure to compatible harnesses.

## Happy path

```bash
free-fleet doctor
free-fleet init my-task --preset classify
# Or bootstrap from labeled data: free-fleet init my-task --from-example labels.csv --label-column label
free-fleet validate my-task --input my-task.sample.jsonl
free-fleet test my-task --input my-task.sample.jsonl
free-fleet run my-task --input my-task.sample.jsonl --run-id my-run
# Zero-key proof (no API keys, deterministic demo/fake route)
free-fleet quickstart --demo --run-id demo --json
```

For tabular data, pass CSV directly (also accepts .txt/.md/.html/.pdf as single-item inputs):
```bash
free-fleet run my-task --input data.csv --id-column id --text-column body --run-id my-run
free-fleet run my-task --input doc.html --run-id my-run
free-fleet run my-task --input paper.pdf --run-id my-run
```
`validate` is offline and warns when `max_slice_chars` triggers sliding-window slicing (lossless overlapping windows, `partial:true`). `test` performs one real inference batch. `run` stores its exact task revision, input digest, typed batches, leases, attempts, sessions, receipts, and inference attempts in SQLite. Long docs beyond `max_slice_chars` are sliding-window sliced with sentence-snapped overlapping windows; quotes must lie within one window.

## Real-time status and benchmark eval

```bash
# Monitor live run progress, batch states, and per-route reliability
free-fleet status my-run --watch
free-fleet status my-run --json

# Benchmark routes on a test sample to update intelligent ranking priors (now parallel)
free-fleet eval my-task --input eval-sample.csv --id-column id --text-column body --concurrency 4
```

## Inspect, resume, and export

```bash
free-fleet tasks --json
free-fleet routes --json
free-fleet cooldowns --json
free-fleet cooldowns --clear
free-fleet routes add ollama/llama3.2:latest --provider ollama --free
free-fleet sessions my-run --json
free-fleet status my-run --json
free-fleet resume my-run --json
free-fleet rescore my-run --input round2.jsonl --run-id my-run-r2 --json
free-fleet history acme --json
free-fleet export my-run --format csv --output results.csv
free-fleet export my-run --format jsonl --output results.jsonl
free-fleet export my-run --format json --output packet.json
free-fleet db backup ./backup.db --json
```

Filters and sorts are typed JSON, validated before anything runs:

```bash
free-fleet export my-run --format csv --output top.csv \
  --filter '{"all": [{"field": "score", "op": ">=", "value": 80}]}' \
  --sort-by score --desc --top 25 --rank
```

`--filter` takes a ClaimFilter document: `all` clauses AND together, `any` holds OR branches, each clause is `{"field", "op", "value"}` with ops `==, !=, >=, <=, >, <, in, not_in` (`in`/`not_in` take a list value). DAG `filter`/`export` nodes take the same shape as `filter`/`sort` mappings.

Resume normally needs only the run ID; free routes remain the default. A paid route approved for an earlier session must be requested again with `--route <route-id>`. SQLite already holds the batch payloads. Do not reconstruct a run from the original files.

## Calibration fitting

Checklist points, source weights, and half-lives start as rubric judgment. Fit them against labeled samples (metadata carries the expected numeric score) with one pinned rater route:

```bash
free-fleet calibrate research-demo --input labeled.jsonl --expected score --route judge/route --json
```

The fit is deterministic coordinate descent (fixed order, no random init) with train/holdout errors reported; points stay on the simplex (non-negative, sum to 100) so tier bands keep their meaning. Relative importance is fitted here; absolute rater level stays in per-route bias correction at export. Dry run by default; `--apply` registers the recalibrated task revision. Restrict with `--params points,weights,halves` and `--max-sweeps 50`.

## Workflows as DAGs

Multi-step flows are typed node kinds, not scripts: `run`, `rescore` (fresh evidence linked to a parent run), `calibrate` (fit + optional register), `filter` (ID sets), `export`. Refs are validated at parse time — `from_run` must name a run/rescore node, `task_from` a calibrate node that applied — and `rescore`/`calibrate` CLI commands are one-node specs over the same runner. A calibrate-then-score funnel in one spec:

```json
{"name": "fit-then-score", "nodes": [
  {"kind": "calibrate", "id": "cal", "task": "score-demo", "input": "labeled.jsonl",
   "route": "judge/route", "params": ["points"], "apply": true},
  {"kind": "run", "id": "scored", "task_from": "cal", "input": "candidates.jsonl"},
  {"kind": "export", "id": "out", "from_run": "scored", "format": "csv", "output": "ranked.csv"}
]}
```

```bash
free-fleet dag --spec funnel.json --dag-id fit1 --json
```

Node run ids are deterministic (`{dag-id}-{node-id}`), so re-running resumes: completed run/rescore nodes are reused and marked `cached`, calibrate nodes reuse their saved report.

Packaged routes are disabled hints, not current price evidence. `routes --refresh` contacts providers and appends observations. Use it only when that mutation is in scope.

## Data & Policy Flags

Runs can be restricted by policy:
- `--free-only`: Explicitly restrict to verified `price_observed_zero` routes (replaces implicit `max-cost=0` sentinel).
- `--zdr`: Enforce zero data retention on provider models.
- `--no-data-collection`: Disallow models that train on inputs.
- `--max-request-cost <amount>`: Upper dollar spend limit per single inference request.
- `--max-cost-in <amount>`: Maximum catalog price per 1k input tokens (deprecated: use --free-only).
- `--max-cost-out <amount>`: Maximum catalog price per 1k output tokens (deprecated: use --free-only).
- `--provider <transport>`: Restrict candidate routes to specific transports (`openrouter`, `opencode`, `openai_compatible`, `demo`).
- `--exclude-provider <transport>`: Exclude specific transports.
- `--openrouter-providers <names>`: Filter OpenRouter upstream routing (supports comma-separated list or repeatable `--openrouter-provider`).
- `--openrouter-order <names>`: Custom ordering for upstream OpenRouter providers (comma-separated or repeatable).
- `--openrouter-ignore <names>`: Upstream OpenRouter hosts to ignore (comma-separated or repeatable).

## Database

The default is `./free-fleet.db`. Select a different database with `--db PATH` or
`ACCOUNT_FLEET_DB`, `FREE_FLEET_DB`, or `BULK_LANES_DB` (the first configured
variable wins).

```bash
free-fleet schema database
```

The queue uses WAL, foreign keys, busy timeout, and atomic `BEGIN IMMEDIATE` leases. Attempt and model-run evidence is retained. Schema version is `"4"`.

## MCP

One-command install is preferred:

```bash
free-fleet mcp install --workspace-root /absolute/workspace --dry-run --json
free-fleet mcp install --workspace-root /absolute/workspace --client auto   # claude|cursor|all
```

Manual entry (if not using `mcp install`):

```json
{
  "mcpServers": {
    "free-fleet": {
      "command": "free-fleet",
      "args": ["serve", "--workspace-root", "/absolute/workspace"]
    }
  }
}
```

The MCP server exposes 13 structured tools:
1. `free_fleet_routes`: List admitted routes, optionally refresh.
2. `free_fleet_cooldowns`: Inspect active rate-limit route cooldowns or clear them.
3. `free_fleet_register_task`: Register an immutable task revision.
4. `free_fleet_tasks`: List registered task definitions.
5. `free_fleet_test`: Run one batch through candidate models (supports `id_column`, `text_column`).
6. `free_fleet_validate`: Offline task and input validation.
7. `free_fleet_run`: Launch bounded resumable campaign (supports `id_column`, `text_column`, `policy`).
8. `free_fleet_resume`: Resume pending batches from existing run.
9. `free_fleet_status`: Real-time batch progress and per-route reliability metrics.
10. `free_fleet_eval`: Benchmark routes against sample inputs and update ranking priors (`concurrency`).
11. `free_fleet_export`: Export clean packet (`format="json"|"csv"|"jsonl"`, typed `claim_filter`/`sort` JSON).
12. `free_fleet_schema`: View JSON Schemas or SQLite database schema.
13. `free_fleet_doctor`: Check workspace health and provider readiness.

The MCP database defaults to `<workspace>/free-fleet.db`. Task, input, database, and packet paths outside the root are refused.

## Stops

- No observed-zero route: inspect route states; do not fall back to billable routes.
- OpenCode missing: install/configure the normal CLI or use configured OpenRouter.
- Typed-input failure: fix the named field or malformed line; do not enable aliases or skipping.
- Typed-output failure: correct the task schema or model response; do not permit extra fields.
- Ambiguous quote: require explicit offsets instead of guessing which occurrence was cited.
- Attempt budget exhausted: report it; do not raise the ceiling without operator direction.
