# account-fleet

[![CI](https://github.com/NatesVibeCode/account-fleet/actions/workflows/ci.yml/badge.svg)](https://github.com/NatesVibeCode/account-fleet/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Python 3.10+](https://img.shields.io/badge/python-3.10%2B-blue.svg)](pyproject.toml)

> **Research and score target accounts with traceable source evidence. Quotes are checked against the source at exact character offsets. Scores and interpretations still need human review.**

Local outbound intelligence engine for high-throughput, evidence-grounded account research across free, paid, and local LLMs — with SQLite checkpointing, 4-layer compounding funnels, and deterministic quote verification.

*Canonical CLI is `account-fleet`. (`free-fleet` remains available as an alias).*

Python 3.10+ is required. This is a command-line tool with an optional AI-assistant integration. It scores source text you supply; the CLI does not browse for companies or fetch job postings automatically. The bundled account-fleet skill guides a connected assistant through that research.

Install and try the offline demo below before running a real list. Real research requires a configured model provider and your own qualification criteria.

---

## 30-Second Example: Raw Accounts In → Scored, Grounded CSV Out

Suppose you have a list of target companies in `accounts.csv`:

```csv
company,careers_text
stripe.com,"We are hiring a Staff Engineer to lead migration off legacy v1 billing pipeline to Kafka..."
hyper_ai,"Looking for Senior Backend Engineer hitting latency limits at 50k QPS on Postgres cluster..."
pinecone.io,"Hiring Infrastructure Engineer scaling vector search across multi-tenant clusters..."
```

### 1. Initialize the account research preset and run

```bash
# Initialize the typed account-research preset (score 0-100, identified_gap, fit_tier)
account-fleet init research-demo --preset account-research

# Process the accounts through free model routes (zero API spend)
account-fleet run research-demo --input accounts.csv --id-column company --text-column careers_text --run-id campaign-01
```

### 2. Export the top 25 ranked accounts

```bash
account-fleet export campaign-01 --format csv --sort-by score --desc --top 25 --rank --output ranked_accounts.csv
```

### 3. Output (`ranked_accounts.csv`)

```csv
rank,item_id,score,identified_gap,fit_tier,primary_quote_text
1,stripe.com,92,"Legacy billing migration",tier_1,"lead migration off legacy v1 billing pipeline to Kafka"
```

Illustrative values only; real exports also include source URLs, digests, and quote details. Set your ICP and scoring rubric through `init --instructions` or a task JSON file. An exact source quote proves the text exists, not that a company will buy your product.

---

## Why You Can Trust the Output

Every output row is gated through deterministic checks *before* it is committed to SQLite. If any check fails, the batch rotates to the next route — nothing unverified is exported.

1. **Deterministic Quote Verification**: Cited quotes are checked against the raw source text at character-level precision and resolved to canonical `[start, end]` offsets. Fabricated or altered quotes fail grounding and trigger immediate route rotation. *(This proves all cited quotes are verbatim source substrings; whether a claim is truly entailed by its quote remains model-generated.)*
2. **Closed JSON Schemas**: Outputs adhere strictly to closed JSON Schemas defined in `TaskSpec`. Models cannot add fields, emit markdown, or drift out of schema.
3. **Intelligent Route Scoring**: Bayesian-smoothed scoring by verification rate, grounding accuracy, malformed-JSON rate, and latency — not round-robin. Best routes are tried first.
4. **Non-Destructive Rate-Limit Handling**: On `429` or `5xx`, the route is cooled down and the batch is retried immediately on the next lane with **0 attempt burn**.
5. **Zero-Price Circuit Breaker & Spend Ceilings**: For zero-price runs, pricing is observed from provider receipts; a non-zero charge trips the breaker and disables the route. For paid runs, `--max-request-cost` enforces per-request caps.

> **Live proof:** `free-fleet status <run_id> --watch` streams batch progress and per-route `Verified / Rate limits / Latency`. Fabricated quotes show up instantly as `grounding_failed` and the next lane is tried.

---

## Quickstart — 60-Second Demo (No API Keys)

```bash
git clone https://github.com/NatesVibeCode/account-fleet.git
cd account-fleet
python3 -m venv .venv
source .venv/bin/activate
python -m pip install .

# Run in your own workspace; output files are written in the current directory.
mkdir my-workspace
cd my-workspace
account-fleet setup
account-fleet quickstart --demo --run-id demo-01

# Outputs:
#   runs/demo-01/clean_packet.json   (self-validating packet)
#   runs/demo-01/clean_packet.csv    (flat CSV)
```

On Windows PowerShell, replace the two virtual-environment commands with `py -m venv .venv` and `.venv\Scripts\Activate.ps1`. If activation is restricted, run `..\.venv\Scripts\account-fleet.exe` directly from `my-workspace`.

The demo uses synthetic scores for ten bundled sample accounts and makes no model API calls. It verifies installation and export, not research quality. Demo routes are excluded from real campaigns unless explicitly selected.

### Real Workspace

```bash
mkdir my-workspace && cd my-workspace
account-fleet setup --workspace-root . --refresh-routes
```

For a real run, configure OpenCode with your own provider access, set `OPENROUTER_API_KEY`, or register a running local model, for example `account-fleet routes add ollama/your-installed-model --provider ollama --free`. Refreshing routes alone does not authenticate you. `account-fleet doctor` checks configuration; `account-fleet test research-demo --input accounts.csv --id-column company --text-column careers_text --provider ollama` tests a real batch before a large campaign.

Each named provider uses its own settings: `OLLAMA_BASE_URL`, `LMSTUDIO_BASE_URL`, `GROQ_API_KEY`, and so on. `OPENAI_COMPATIBLE_BASE_URL` and `OPENAI_COMPATIBLE_API_KEY` configure only `--provider openai_compatible`. Environment variables must be available to the process running the CLI or MCP server; `.env` files are not loaded automatically.

`setup` installs both the account-fleet research skill and the free-fleet execution skill in the workspace's `.agents/skills` directory. Keep the virtual environment in place when using the generated MCP configuration. Install account-fleet and free-fleet in separate environments: they share the `free_fleet` Python package and compatibility commands.

### Presets

Create typed tasks instantly with built-in presets:

```bash
free-fleet init score-demo --preset score             # Numerical 0-100 fit score + evidence
free-fleet init filter-demo --preset filter           # Boolean qualification pass/fail gate
free-fleet init account-demo --preset account-research # ICP scoring + technical gap extraction
free-fleet init triage-demo --preset triage           # Priority (high/medium/low) + reason
free-fleet init classify-demo --preset classify       # Categorical labels + summary
free-fleet init extract-demo --preset extract         # Named entities + summary
free-fleet init summarize-demo --preset summarize     # Supported fact summaries
```

Validate and test before launching large runs:

```bash
# Validate task spec and input without making any API calls
free-fleet validate score-demo --input input.jsonl

# Test a single real batch
free-fleet test score-demo --input input.jsonl
```

---

## Core Capabilities

### 1. CSV In / Scored, Ranked CSV Out
Directly process tabular data and export sorted, ranked deliverables with exact source quotes:

```bash
# Run on CSV specifying ID and text columns (or let free-fleet auto-detect them)
free-fleet run score-demo --input accounts.csv --run-id accts-01

# Export ranked deliverable: sorted by score descending, top 25, with 1-indexed rank column
free-fleet export accts-01 --format csv --sort-by score --desc --top 25 --rank --output ranked_target_accounts.csv
```

### 2. The Compounding Filter (Chaining Layers)
Run multi-stage funnel filtering without running monolithic prompts or wasting model compute:

```bash
# Layer 1: Filter down to survivors
free-fleet run l1-task --input 1000_candidates.csv --run-id l1
free-fleet export l1 --format csv --filter "passed=true" --output l1_survivors.csv

# Layer 2: Only run on survivor IDs from Layer 1
free-fleet run l2-task --input tech_docs.csv --only-ids l1_survivors.csv --run-id l2
free-fleet export l2 --format csv --filter "passed=true" --output l2_survivors.csv

# Final Layer: Score survivors and rank top candidates
free-fleet run l3-task --input gap_analysis.csv --only-ids l2_survivors.csv --run-id l3
free-fleet export l3 --format csv --sort-by score --desc --top 25 --rank --output ranked_deliverable.csv
```

### 3. Live Run Monitoring
Track queue progress, worker concurrency, and route-level metrics in real time:

```bash
free-fleet status <run_id> --watch
```

Output:
```
============================================================
Run: triage-01  |  Task: customer-triage  |  Status: RUNNING
Progress: [=========================>              ] 62.5% (650/1040)
============================================================
Batches:
  Pending:    15
  Leased:      4
  Done:       65
  Failed:      0

Route Performance:
  openrouter:qwen/qwen-2.5-72b-instruct:free
    Attempts: 45 | Verified: 44 | Rate limits: 1 | Latency: 1.2s
  openrouter:meta-llama/llama-3.3-70b-instruct:free
    Attempts: 24 | Verified: 23 | Rate limits: 0 | Latency: 1.8s
```

### 3. Continuous Route Evaluation
Benchmark available routes against test datasets to determine which models excel at your specific task:

```bash
free-fleet eval customer-triage --input test-samples.csv --id-column id --text-column comment
```

Output:
```
========================================================================================
Route Evaluation Benchmark
Task: customer-triage  |  Samples: 20
========================================================================================
Route                                      Success   Grounding   Score    Avg Latency
----------------------------------------------------------------------------------------
openrouter:qwen/qwen-2.5-72b-instruct:free   100.0%     100.0%    0.982          1.15s
openrouter:meta-llama/llama-3.3-70b-free      95.0%      90.0%    0.871          1.82s
opencode:llama3                               80.0%      85.0%    0.742          2.40s
```
Evaluation benchmarks automatically update route selection priors for subsequent runs.

### 4. Local Models & Generic OpenAI-Compatible Providers
Run bulk workloads completely locally with **Ollama**, **LM Studio**, **vLLM**, or fast cloud inference providers like **Groq** and **Cerebras**:

```bash
# Register your local or custom route in the catalog
free-fleet routes add ollama/llama3.2:latest --provider ollama --free

# Or configure environment variables
export OPENAI_COMPATIBLE_BASE_URL="http://localhost:11434/v1"
export OPENAI_COMPATIBLE_API_KEY="ollama"
export OPENAI_COMPATIBLE_MODEL="llama3.2:latest"

# Run with local provider selection
free-fleet run my-task --input data.csv --id-column id --text-column text --provider ollama
```

Endpoints on `localhost` or `127.0.0.1` are automatically marked free (`cost = 0.0`). For third-party cloud OpenAI-compatible endpoints, specify costs explicitly (`--input-cost` / `--output-cost`) or leave them as unknown-cost to prevent accidental misclassification.

### 5. Explicit Data & Privacy Policy
Enforce zero data retention (ZDR), prohibit provider data collection, limit request spend, and control upstream routing on a per-run basis:

```bash
free-fleet run my-task \
  --input sensitive-data.jsonl \
  --zdr \
  --no-data-collection \
  --provider openrouter \
  --exclude-provider opencode \
  --openrouter-providers Anthropic,Together \
  --max-request-cost 0.05
```

You can pass `--openrouter-providers` as a comma-separated list or as repeatable `--openrouter-provider` flags.

---

## SQLite Control Plane

`free-fleet` uses SQLite in WAL mode with `BEGIN IMMEDIATE` atomic leases. If a worker crashes or a laptop closes, the run can be resumed seamlessly:

```bash
free-fleet resume <run_id>
```

- **Resumable**: Batches are committed upon verification. Completed work is never repeated.
- **Fault-Tolerant**: Stale worker leases are automatically recovered after timeout.
- **Concurrent**: Multiple worker processes can safely lease batches simultaneously without collisions.
- **Auditable**: Every attempt, model receipt, cost observation, and verification failure is recorded immutably in `inference_attempts`.

---

## Commands

| Command | Purpose |
|---|---|
| `quickstart` | One-command offline demo (no keys) that writes a verified packet + CSV |
| `setup` | Bootstrap a portable workspace with bundled skills and SQLite database |
| `doctor` | Check SQLite, installed CLIs, provider authentication, and available routes |
| `routes` | List or refresh discovered model routes (`--refresh`) |
| `routes add` | Register an explicit custom or local model route (`--free`, `--input-cost`) |
| `cooldowns` | Inspect active rate-limit route cooldowns or clear them (`--clear`, `--route`) |
| `tasks` | List registered task definitions |
| `init` | Create a typed task from a preset (`score`, `filter`, `account-research`, `triage`, `classify`, `extract`, `summarize`) |
| `init --from-example` | Infer a draft `claims_schema` from a labeled CSV (`--from-example labels.csv --label-column label`) |
| `validate` | Check task schema and input formatting without inference (`--only-ids`) |
| `test` | Run one real batch through candidate models |
| `run` | Create and execute a SQLite-backed resumable run (`--only-ids` for compounding filter) |
| `resume` | Resume an unfinished run from its SQLite queue |
| `status` | Show real-time progress, attempts, and route stats (`--watch`, `--json`) |
| `eval` | Benchmark routes on sample inputs and update route ranking priors (`--concurrency`) |
| `sessions` | Inspect recorded worker sessions and audit logs |
| `export` | Export a validated packet (`--format json\|csv\|jsonl`, `--sort-by`, `--desc`, `--top`, `--rank`, `--filter`) |
| `db backup` | SQLite backup to file (safe while running) |
| `schema` | Print admitted JSON Schemas or database contracts |
| `mcp install` | One-command Claude/Cursor setup (auto-wires `claude_desktop_config.json` / `mcp.json`) |
| `serve` | Run the Model Context Protocol (MCP) server over stdio |
| `discover` | Broad web search (`ddgs`, self-hosted SearXNG, HN Algolia, YC, Reddit, Stack Exchange, Discourse, Lobsters, Lemmy, Dev.to) to an accounts file |
| `fetch` | Fetch URLs, sitemaps, site crawls, ATS boards (Greenhouse/Ashby/Lever), YC profiles, HN/Reddit threads, or Q&A forums to an accounts file |

Pass `--json` to any command for machine-readable JSON output. `--free-only` is the explicit zero-cost filter (replaces implicit `max-cost=0` sentinel). Long documents are warned when truncated (`partial` slices).


---

## MCP Server

`free-fleet` includes a Model Context Protocol (MCP) server for integration into Cursor, Claude Desktop, Antigravity, and other agent environments:

**One-command install (recommended for GTM folks):**
```bash
free-fleet mcp install --workspace-root "$PWD"  # auto-detects Claude/Cursor, writes mcpServers entry
# Preview first
free-fleet mcp install --dry-run --json
free-fleet doctor --workspace-root "$PWD" --json  # verify
# Restart Claude/Cursor to load
```

Manual entry:
```json
{
  "mcpServers": {
    "free-fleet": {
      "command": "free-fleet",
      "args": ["serve", "--workspace-root", "/absolute/path/to/workspace"]
    }
  }
}
```

---

## Verification & Testing

Run the test suite:

```bash
pytest -q
```

All core components (Bayesian route scoring, SQLite control plane, rate limit cooldowns, OpenAI-compatible provider, CSV IO, real-time status monitoring, and route evals) are covered by automated unit and integration tests.

---

## License

MIT. See [LICENSE](LICENSE).
