# Autonomous ICP Inference & Calibration Guide

When an operator, founder, or sales leader wants to run target account prospecting or scoring, **never start by interrogating them with generic questions**, and **never jump blindly into web searches with vague industry keywords** (e.g. *"B2B SaaS"*, *"fintech companies"*).

An intelligent agent harness must first inspect all available context—past session transcripts, agent memories, and the local repository/codebase—to autonomously infer the company's product, technical stack, target personas, and architectural boundaries.

Only after this autonomous discovery phase should the harness perform a gap analysis and conduct a focused, 30-second interview for anything missing.

---

## 1. Autonomous Pre-Interview Discovery (History, Memory, & Repo First)

Before prompting the operator, the harness executes these autonomous discovery steps:

### A. Inspect Past Sessions, Transcripts, & Agent Memories
- **Transcripts**: Use the current conversation and relevant history explicitly supplied by the user. There is no required history directory; missing history must not block a fresh install.
- **Memory & Scratchpads**: Use relevant context already available within the user's selected project. Do not search unrelated personal conversations.
- **Search Patterns**: Grep for customer discussions, target industries, competitor comparisons, and pricing:
  ```bash
  grep -Ei "customer|client|target|prospect|competitor|pricing|enterprise" <transcript_path>
  ```
- **Extract**: Prior customer names, product positioning statements, competitor names being replaced, and target account tiers already mentioned by the operator.

### B. Inspect Workspace Codebase & Architecture
- **Root Metadata**: Read `README.md`, `pyproject.toml`, `package.json`, `Cargo.toml`, or `go.mod` to determine what the product actually does and where it runs.
- **Integration Connectors & Client SDKs**:
  - Scan package dependencies, imports, and integration directories (`connectors/`, `providers/`, `adapters/`, `integrations/`).
  - *Deduction Logic*:
    - If the codebase imports `psycopg2`, `asyncpg`, or `sqlalchemy` -> Target accounts run PostgreSQL.
    - If it imports `confluent-kafka` or `aiokafka` -> Target accounts run Apache Kafka streaming.
    - If it imports `boto3` or `google-cloud-*` -> Target accounts operate on AWS or GCP.
    - If it imports `kubernetes` or defines Helm charts -> Target accounts run Kubernetes clusters.
- **CLI Commands & Entrypoints**: Inspect CLI definitions or API routes to identify core verbs (e.g. `optimize`, `cache`, `monitor`, `filter`, `replicate`).
- **Git Commit History**: Run `git log -n 15 --oneline` to see recent architectural focus, bug fixes, and user-facing feature additions.

---

## 2. The 6 Target Signals: What They Mean & How to Gather Them

The harness must systematically resolve these 6 core signals:

### Signal 1: Architectural Layer & Insertion Point
- **What it means**: Where in the target customer's technology stack does our product live? (e.g. In-process library/SDK, sidecar proxy, kernel eBPF agent, database proxy, cloud middleware, CI/CD runner, SaaS API).
- **How to gather it**: Check architecture docs, installation guides, Dockerfiles, and runtime requirements.
- **How to think about it**: The insertion point dictates feasibility. If our product is an eBPF profiling agent, the prospect *must* operate self-managed Linux nodes or Kubernetes clusters; companies running purely on serverless functions (e.g. AWS Lambda / Vercel) cannot install it.

### Signal 2: Non-Negotiable Upstream & Downstream Dependencies (Required Stack)
- **What it means**: The specific infrastructure or tools the prospect *must* already have in production for our product to deliver value.
- **How to gather it**: Grep for third-party client libraries, database drivers, cloud provider SDKs, and config schemas in the repo. Check past session discussions.
- **How to think about it**: If our tool optimizes PostgreSQL query plans, a prospect running exclusively DynamoDB or MongoDB has zero utility for us, regardless of their revenue or employee count. This forms our primary search operator (e.g. `"PostgreSQL"`).

### Signal 3: Disqualifiers & Anti-ICP (Negative Stack Exclusions)
- **What it means**: Stacks, architectures, or firmographics that make an account an immediate misfit, even if other criteria match.
- **How to gather it**: Look at unsupported features in docs, explicit non-goals, or competitor platforms we do not integrate with.
- **How to think about it**: Disqualifiers save significant API costs and human review time by eliminating false positives before scoring. For example, if we require self-hosted Kafka, cloud-managed Confluent Serverless without VPC peering might be a negative exclusion.

### Signal 4: The Operational Breaking Point / Catalyst Event
- **What it means**: The specific technical threshold, scaling bottleneck, bill shock, or architectural migration where the status quo fails and engineering leadership has urgent budget/authorization to buy.
- **How to gather it**: Look at problem descriptions in repo READMEs, issue discussions, marketing copy, or past founder discussions.
- **How to think about it**: Nobody buys infrastructure software because they are bored; they buy because:
  - *Scale / Latency*: p99 query latency hit 500ms at 50k QPS causing checkout timeouts.
  - *Bill Shock*: Snowflake/Databricks or Redis compute bills spiked 300% month-over-month.
  - *Migration*: The team is decomposing a monolithic Rails/MySQL app into Go microservices + Kafka.
  - *Compliance*: SOC2 Type II or HIPAA audit is due in 90 days.
  This exact trigger phrase is what we verify with character-exact quotes in Tier 1 (85–100) scoring.

### Signal 5: Target Engineering Personas & Job Titles
- **What it means**: The exact job titles of the engineers who experience the bottleneck daily and the managers/executives who sign the purchase order.
- **How to gather it**: Map the required stack and pain point to standard engineering hierarchy:
  - Database pain -> `Database Reliability Engineer (DBRE)`, `Staff Backend Engineer`, `Head of Infrastructure`
  - Streaming/Data pain -> `Staff Data Platform Engineer`, `Lead Data Architect`
  - Cloud/Kubernetes pain -> `Site Reliability Engineer (SRE)`, `DevOps Lead`, `VP of Platform`
  - Security pain -> `Director of Information Security`, `Head of SecOps`
- **How to think about it**: Job postings for these roles contain the raw, unvarnished descriptions of current architectural fires.

### Signal 6: Anchor Customers / Reference Logos
- **What it means**: 2–3 existing happy customers or aspirational "dream accounts" that exemplify the perfect buyer.
- **How to gather it**: Check case studies in docs, customer testimonials, test fixtures, or search session transcripts for named companies.
- **How to think about it**: Reference logos allow the harness to reverse-engineer their real job postings on Ashby/Greenhouse, discovering the exact vocabulary, tools, and phrasing real engineering teams use.

---

## 3. The Gap Analysis & Delta Determination

After running autonomous discovery, the harness evaluates each signal:

```
[Signal Status Scorecard]
1. Architectural Layer:   [ CONFIRMED | INFERRED | MISSING ]
2. Required Tech Stack:   [ CONFIRMED | INFERRED | MISSING ]
3. Negative Exclusions:   [ CONFIRMED | INFERRED | MISSING ]
4. Target Personas:       [ CONFIRMED | INFERRED | MISSING ]
5. Breaking Point Catalyst: [ CONFIRMED | INFERRED | MISSING ]
6. Anchor Logos:          [ CONFIRMED | INFERRED | MISSING ]
```

### The Invariant:
**NEVER ask the operator about signals that are already CONFIRMED or obvious from their repo and past sessions.**
Only interview for signals that are `MISSING` or ambiguous.

---

## 4. The Targeted Gap Interview

When any of the 6 signals remain missing, present the inferred foundation first, then ask only for the missing pieces:

> *"I scanned your codebase, dependencies, and past session history. Here is what I already inferred about your company profile:*
> - **Product Domain & Layer**: Database query optimization & connection pooling *(from README.md & repo architecture)*
> - **Required Tech Stack**: PostgreSQL, Kubernetes, AWS *(from dependencies & connectors)*
> - **Negative Exclusions**: Pure NoSQL / Firebase architectures *(from docs)*
> - **Target Personas**: Staff Infrastructure Engineer, DBRE *(from architecture scope)*
> 
> *To calibrate your 0–100 scoring rubric and discovery queries, I only need to clarify [1 or 2 missing details]:*
> 1. **Breaking Point Catalyst**: What is the exact technical trigger where someone has to buy your product? (e.g. query latency limits at 50k QPS, Redis memory bills > $10k/mo, or migrating off legacy billing?)
> 2. **Anchor Logos**: Who are 2 or 3 of your happiest existing customers or dream accounts? (e.g. Stripe, PostHog, Supabase)"*

Once answered, the harness immediately has all 6 signals locked.

---

## 5. Reverse-Engineering Anchor Logos & Formulating ATS Queries

Using the anchor logos (either discovered or confirmed in the interview):
1. Search their live job posts on Ashby, Greenhouse, and Lever:
   - `site:jobs.ashbyhq.com/anchorcompany`
   - `site:boards.greenhouse.io/anchorcompany`
2. Extract the unedited engineering phrases they use to describe the stack, scale, and pain.
3. Formulate discovery search operators using the exact keywords:
   ```text
   site:jobs.ashbyhq.com "[RequiredStack]" ("[TriggerPhrase1]" OR "[TriggerPhrase2]")
   site:boards.greenhouse.io "[RequiredStack]" ("[TargetRole]" AND "[TriggerPhrase1]")
   ```

---

## 6. Synthesize the Calibrated Profile & 0–100 Rubric

Output the finalized profile and rubric into a typed configuration:

```json
{
  "icp_profile": {
    "product_category": "Database Performance & Query Optimization",
    "architectural_layer": "Postgres proxy & connection pooler",
    "required_stack": ["PostgreSQL", "Kafka", "Kubernetes"],
    "negative_stack_exclusions": ["MongoDB", "DynamoDB", "Firebase"],
    "trigger_pain_phrases": [
      "query latency limits",
      "50k QPS",
      "connection pool exhaustion",
      "read replica lag",
      "legacy billing migration"
    ],
    "target_roles": [
      "Staff Infrastructure Engineer",
      "Senior Database Reliability Engineer",
      "Lead Platform Architect"
    ],
    "anchor_logos": ["stripe.com", "posthog.com", "supabase.com"]
  },
  "calibrated_scoring_rubric": {
    "tier_1_score_85_100": {
      "definition": "Explicit technical bottleneck cited verbatim in an active job post or changelog (e.g. 'hitting latency limits at 50k QPS on Postgres').",
      "evidence_rule": "Must cite verbatim sentence containing stack + active pain."
    },
    "tier_2_score_70_84": {
      "definition": "Confirmed target stack (Postgres + Kafka) and actively hiring Staff/Senior infrastructure roles, but specific bottleneck is implied rather than explicitly named.",
      "evidence_rule": "Must cite verbatim sentence proving stack presence and senior hiring."
    },
    "tier_3_score_50_69": {
      "definition": "Company matches firmographic criteria (B2B SaaS / Tech) but no evidence of high-scale database pain.",
      "evidence_rule": "General company overview quote."
    },
    "unfit_score_0_49": {
      "definition": "Uses competing or incompatible tech stack (e.g. purely managed NoSQL) or outside target domain.",
      "evidence_rule": "None (auto-discarded from final deliverable)."
    }
  }
}
```

With this complete specification, proceed immediately to Phase 2 (Discovery) and Phase 3 (Batch Qualification Gate).

Save the confirmed `icp_profile` fields as the typed account profile before
starting discovery. The JSON is the editable authoring file; SQLite stores the
immutable revision and active pointer:

```bash
account-fleet profile --init
# edit ideal_company_profile.json with the confirmed profile
account-fleet profile
```
