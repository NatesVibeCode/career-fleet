# Target Account Research & ICP Scoring Example

This example demonstrates how to research, qualify, score, and rank target accounts with **character-exact source evidence**, matching the carousel presentation:

1. **Target Account Research & ICP Scoring**: Scores accounts 0–100 and clips verbatim evidence of technical gaps or bottlenecks.
2. **The 4-Layer Compounding Filter**: Filter large candidate pools down to qualified survivors at each stage without wasting model compute.
3. **Verifiable Ranked Deliverable**: Export a clean, sorted CSV (`ranked_target_accounts.csv`) with rank, fit score, and source quotes ready for CRM import.

---

## Files in this Directory

- `sample_accounts.csv`: Sample accounts with authentic engineering job postings and infrastructure initiatives (Stripe, Hyper AI, Pinecone, PostHog, Supabase, etc.).
- `task.json`: Pre-configured closed task specification requiring a numeric `score` (0–100), `fit_tier`, `identified_gap`, and `reasoning`.
- `ideal_company_profile.json`: Typed ICP profile that can be persisted and attached to a run for auditability.

---

## Quickstart (CLI)

### 1. Initialize Task from Preset
You can initialize directly from the built-in `account-research` (or `score`) preset:

```bash
harness-fleet init account-research --preset account-research

# Persist the ICP in the same SQLite database before running
account-fleet profile --path examples/account_research/ideal_company_profile.json --db harness-fleet.db
```

Or register the bundled `task.json`:
```bash
harness-fleet run examples/account_research/task.json \
  --input examples/account_research/sample_accounts.csv \
  --run-id accounts-01
```

### 2. Export Ranked Target Accounts CSV

Generate the exact deliverable shown in Slide 3 (sorted by score descending, top survivors, with a 1-indexed rank column):

```bash
harness-fleet export accounts-01 \
  --format csv \
  --sort-by score \
  --desc \
  --top 5 \
  --rank \
  --output ranked_target_accounts.csv
```

Output (`ranked_target_accounts.csv`):
```csv
rank,item_id,score,fit_tier,identified_gap,reasoning,primary_quote_text,quote_count,source_uri,source_digest
1,stripe.com,98,tier_1,"Legacy billing pipeline migration to Kafka","Staff engineer posting mentions leading Kafka migration","lead migration off legacy v1 billing pipeline to Kafka",1,"",...
2,hyper_ai,94,tier_1,"Postgres latency at 50k QPS","Database engineer job cites latency bottlenecks","hitting latency limits at 50k QPS on Postgres cluster",1,"",...
3,pinecone.io,91,tier_1,"Multi-tenant vector search scaling","Scaling search across multi-tenant clusters","scaling vector search across multi-tenant clusters",1,"",...
```

---

## The 4-Layer Compounding Filter (Slide 4)

To filter 1,000 accounts down to 25 without running monolithic prompts:

```bash
# Layer 1: Firmographic fit screening (1,000 -> 600)
harness-fleet init l1-filter --preset filter
harness-fleet run l1-filter --input homepages.csv --run-id l1-run
harness-fleet export l1-run --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l1_survivors.csv

# Layer 2: Tech stack & architecture screening (600 -> 150)
harness-fleet init l2-filter --preset filter
harness-fleet run l2-filter --input tech_docs.csv --only-ids l1_survivors.csv --run-id l2-run
harness-fleet export l2-run --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l2_survivors.csv

# Layer 3: Hiring & budget signals (150 -> 50)
harness-fleet init l3-filter --preset filter
harness-fleet run l3-filter --input job_posts.csv --only-ids l2_survivors.csv --run-id l3-run
harness-fleet export l3-run --format csv --filter '{"all": [{"field": "passed", "value": true}]}' --output l3_survivors.csv

# Layer 4: ICP scoring & verbatim evidence (50 -> 25)
harness-fleet init l4-scoring --preset score
harness-fleet run l4-scoring --input qualified_profiles.csv --only-ids l3_survivors.csv --run-id l4-run
harness-fleet export l4-run --format csv --sort-by score --desc --top 25 --rank --output ranked_target_accounts.csv
```

---

## Chat & MCP Integration (Claude Desktop, Cursor, Antigravity)

When using `harness-fleet` via MCP, chat agents can execute this entire flow in three turns:

1. **Initialize Task**: Call `harness_fleet_init(task_name="prospecting", preset="score")`
2. **Execute Run**: Call `harness_fleet_run(task="prospecting", input_path="sample_accounts.csv", run_id="run-01")`
3. **Export Ranked Deliverable**: Call `harness_fleet_export(run_id="run-01", export_format="csv", sort_by="score", top_n=25, rank=True, output_path="ranked_target_accounts.csv")`
