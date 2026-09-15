---
name: career-fleet
description: "Operate Career Fleet for generic users: install it, create a profile, run discovery and screening lanes, export qualified dossiers, and troubleshoot common failures."
---

# Career Fleet: Onboarding and Operations

Use this skill when someone needs to install, configure, run, or explain the Career Fleet command-line workflow. The goal is a reproducible workspace containing a profile, local SQLite database, captured source text, lane evaluations, and exportable dossiers.

Keep the user's profile authoritative. Do not invent personal constraints or silently substitute a default profile. The generated profile is neutral until the user edits it. Workplace policy is optional and defaults to `any`; never assume that a user wants remote or hybrid work.

Treat `profile.json` as the editable authoring file and SQLite as the durable
IEP record: screening runs persist an immutable profile revision and attach it
to each evaluation.

---

## The 4-Lane Funnel Architecture

```
0. Context Inference & IEP Calibration ──> Lane 1: Sourcing (YC, ATS, site, community)
                                                        │
                                                        ▼
Lane 4: Founder & Culture Recon        <── Lane 3: Systems Wedge <── Lane 2: Gatekeeper Triage
          │                                                                 │
          ▼                                                                 ▼
   Qualified Dossier                                              Dropped Dealbreakers
```

---

## Operating rules

- Read [references/onboarding.md](references/onboarding.md) for a new installation or a fresh workspace.
- Read [references/workflow.md](references/workflow.md) before running discovery, triage, recon, or export.
- Read [references/troubleshooting.md](references/troubleshooting.md) when a command fails, returns no results, or behaves unexpectedly.
- Keep discovery, triage, and recon pointed at the same `--db` and `--profile` paths.
- Use the pre-filled `career_sources.json` and `career-fleet discover --source community` for the standard community pass. Treat that visible file as the source of truth; use manual flags only for explicit overrides.
- Treat a company as finally qualified only after Lane 3 passes and Lane 4 reaches the healthy threshold; inspect the dossier's source quotes before relying on it.
- Check command exit codes. A source failure is not a completed discovery run.

## Phase 0: Authorized Context Inference & Targeted IEP Calibration

Never interrogate the user with 20 generic questions. Execute a two-step context protocol:

### Step 1: Authorized Context Discovery (Resume, Projects & Codebase When Supplied)
Before prompting the user:
1. Inspect only documents, repositories, GitHub activity, or commit history that the user explicitly provides or authorizes. Never scan unrelated workspace files or external accounts by assumption.
2. Infer the 6 Core Signals:
   - *Architectural Wedge*: What high-impact problem does the candidate solve? (e.g. 0-to-1 GTM systems, distributed runtimes, ML data pipelines).
   - *Required Tech Stack*: What must the company run? (e.g. Python, Go, PostgreSQL, Kafka, Kubernetes).
   - *Workplace Preference*: Only if the user states one: no restriction (`any`), remote-only, or remote-or-hybrid.
   - *Negative Exclusions / Dealbreakers*: Disqualifiers (e.g. 5-day non-local office mandates, bloated headcounts, prompt wrappers).
   - *Hiring Catalyst*: What acute operational pain creates the role? (e.g. scaling bottlenecks, moving past founder-led sales).
   - *Target Leader Persona*: Who does the candidate partner with? (e.g. low-ego technical builders).
   - *Anchor Exemplars*: 2–3 dream companies defining the standard.

### Step 2: Gap Analysis & 3-Question Interview
Ask **only** for signals that are genuinely unobserved or ambiguous. Confirm the inferred profile with the user before writing it to `profile.json`. See [references/iep-interview.md](references/iep-interview.md).

---

## Phase 1: Lane Execution

* **Lane 1: Sourcing (`career-fleet discover`)**: Ingests companies from YC batches and direct ATS boards (Ashby, Greenhouse, Lever), crawls a site, or captures career-focused evidence from Reddit, HN, Stack Exchange, Discourse, Lobsters, Lemmy, and Dev.to. Community items without one unambiguous external company domain remain durable unlinked leads and are listed with `career-fleet signals`.
* **Lane 2: Gatekeeper Triage (`career-fleet triage`)**: Drops dealbreakers instantly (headcount limits, in-person office mandates, shallow wrappers, pure quota roles) without model cost.
* **Lane 3: Systems Wedge (`career-fleet recon --lane systems`)**: Evaluates proprietary technical defensibility and infrastructure depth with source quotes.
* **Lane 4: Culture Recon (`career-fleet recon --lane culture`)**: Assesses leadership signals and communication culture from captured source text.

For the detailed signal framework and interview prompts, read [references/6-core-career-signals.md](references/6-core-career-signals.md) and [references/iep-interview.md](references/iep-interview.md). Use [references/scoring-rubric-guide.md](references/scoring-rubric-guide.md) when explaining scores.

## Related skills

- `harness-fleet` — the shared engine underneath every fleet playbook: task contracts, runs, export, MCP, troubleshooting.

Setup installs these next to this skill in `.agents/skills/`.
