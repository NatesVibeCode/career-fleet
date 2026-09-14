# MCP & CLI Agent Execution Recipes

This guide contains step-by-step execution recipes for AI agents (Codex, Claude, Cursor, Antigravity) executing account research tasks.

---

## Recipe 1: Full Loop from Conversational ICP (Discovery ──> Top 25 Deliverable)

### User Prompt:
> *"I sell database performance monitoring to high-scale B2B tech companies. Find me 25 qualified accounts and give me proof of their bottlenecks."*

### Agent Execution Protocol:

#### Step 0: Save the Ideal Company Profile
Create or update `ideal_company_profile.json` from the confirmed ICP, then
persist it before registering or running the task. Use `--init` only when the
file does not exist:

```bash
# first time only
account-fleet profile --init
# edit ideal_company_profile.json
account-fleet profile
```

The SQLite database keeps the immutable profile revision so later runs can be
audited against the exact ICP that was active.

#### Step 1: Deconstruct the ICP
- Architecture: `PostgreSQL`, `MySQL`, `ClickHouse`, `Redis`
- Bottlenecks: `latency limits`, `slow queries`, `connection pooling`, `QPS ceiling`, `50k QPS`
- Role: `Staff Infrastructure Engineer`, `Senior Backend Engineer`

#### Step 2: Run Discovery Queries (via Web Search / Scrape)
Run queries across Ashby and Greenhouse:
- `site:jobs.ashbyhq.com "PostgreSQL" ("latency" OR "scaling" OR "queries")`
- `site:boards.greenhouse.io "Postgres" ("50k QPS" OR "connection pool")`

Save extracted snippets and URLs into `accounts.csv`:
```csv
item_id,text,source_uri
stripe.com,"...migration of our legacy billing service to Kafka...","https://jobs.ashbyhq.com/stripe/staff-eng"
hyper_ai,"...hitting latency limits at 50k QPS on Postgres cluster...","https://boards.greenhouse.io/hyperai/infra"
```

#### Step 3: Initialize the Account Task Contract
Via MCP tool `free_fleet_init` (or CLI `account-fleet init`):
```json
{
  "task_name": "db-monitoring-qualification",
  "preset": "account-research",
  "instructions": "Evaluate target accounts for database monitoring fit. Score 85-100 if the company explicitly mentions high QPS, latency bottlenecks, or database scaling in PostgreSQL/MySQL. Require exact verbatim quotes of the bottleneck. Score < 50 if no database scaling issues exist."
}
```

#### Step 4: Run the Campaign with Free Routes
Via MCP tool `free_fleet_run` (or CLI `account-fleet run`):
```json
{
  "task": "db-monitoring-qualification",
  "input_path": "accounts.csv",
  "run_id": "db-campaign-01",
  "id_column": "item_id",
  "text_column": "text",
  "profile_path": "ideal_company_profile.json",
  "sessions": 4,
  "policy": {
    "free_only": true
  }
}
```

#### Step 5: Export the Top 25 Ranked CSV
Via MCP tool `free_fleet_export` (or CLI `account-fleet export`):
```json
{
  "run_id": "db-campaign-01",
  "export_format": "csv",
  "sort": {"field": "score", "descending": true},
  "top_n": 25,
  "rank": true,
  "output_path": "top_25_ranked_accounts.csv"
}
```

#### Step 6: Present Deliverable Table to User
Show the top 5 directly in the chat with quotes and links, and provide the path to `top_25_ranked_accounts.csv`.

---

## Recipe 2: The 4-Layer Compounding Funnel (1,000 Accounts ──> Top 25)

When processing large input lists (e.g. 1,000 accounts from an Apollo or conference export), run compounding layers to avoid wasting model compute on unqualified leads:

```bash
# Layer 1: Firmographic Filter (1,000 -> 600)
account-fleet run l1-filter --input raw_1000.csv --run-id l1-pass
account-fleet export l1-pass --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l1_survivors.csv

# Layer 2: Architecture Stack Screen (600 -> 150)
account-fleet run l2-stack --input tech_docs.csv --only-ids l1_survivors.csv --run-id l2-pass
account-fleet export l2-pass --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l2_survivors.csv

# Layer 3: Hiring & Urgency Signals (150 -> 50)
account-fleet run l3-hiring --input job_posts.csv --only-ids l2_survivors.csv --run-id l3-pass
account-fleet export l3-pass --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l3_survivors.csv

# Layer 4: Deep Scoring & Verbatim Proof (50 -> Top 25)
account-fleet run l4-scoring --input verified_sources.csv --only-ids l3_survivors.csv --run-id l4-final
account-fleet export l4-final --format csv --sort-by score --desc --top 25 --rank --output ranked_accounts.csv
```
