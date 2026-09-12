---
name: career-fleet
description: Autonomous career & employer intelligence skill. Discovers, triages, and scores companies against an Ideal Employer Profile (IEP) with exact character-offset quote verification across 4 compounding lanes.
---

# Career Fleet: Autonomous Career & Employer Intelligence Skill

Turn an engineer, operator, or founder's background and career criteria into a rigorously evaluated pipeline of target companies and high-leverage opportunities, backed by verbatim quotes from job postings and engineering architecture documents.

---

## The 4-Lane Funnel Architecture

```
0. Context Inference & IEP Calibration ──> Lane 1: Sourcing (YC, VC, ATS)
                                                        │
                                                        ▼
Lane 4: Founder & Culture Recon        <── Lane 3: Systems Wedge <── Lane 2: Gatekeeper Triage
          │                                                                 │
          ▼                                                                 ▼
   Qualified Dossier                                              Dropped Dealbreakers
```

---

## Phase 0: Autonomous Context Inference & Targeted IEP Calibration

Never interrogate the user with 20 generic questions. Execute a two-step context protocol:

### Step 1: Autonomous Discovery (Resume, Projects & Codebase First)
Before prompting the user:
1. Inspect the candidate's provided documents (resumes, project portfolios, GitHub activity, commit history).
2. Infer the 6 Core Signals:
   - *Architectural Wedge*: What high-impact problem does the candidate solve? (e.g. 0-to-1 GTM systems, distributed runtimes, ML data pipelines).
   - *Required Tech Stack*: What must the company run? (e.g. Python, Go, PostgreSQL, Kafka, Kubernetes).
   - *Negative Exclusions / Dealbreakers*: Disqualifiers (e.g. 5-day non-local office mandates, bloated headcounts, prompt wrappers).
   - *Hiring Catalyst*: What acute operational pain creates the role? (e.g. scaling bottlenecks, moving past founder-led sales).
   - *Target Leader Persona*: Who does the candidate partner with? (e.g. low-ego technical builders).
   - *Anchor Exemplars*: 2–3 dream companies defining the standard.

### Step 2: Gap Analysis & 3-Question Interview
Ask **only** for signals that are genuinely unobserved or ambiguous. See [references/iep-interview.md](references/iep-interview.md).

---

## Phase 1: Lane Execution

* **Lane 1: Sourcing (`career-fleet discover`)**: Ingests companies from YC batches, VC seed portfolios, and direct ATS boards (Ashby, Greenhouse, Lever).
* **Lane 2: Gatekeeper Triage (`career-fleet triage`)**: Drops dealbreakers instantly (headcount limits, in-person office mandates, shallow wrappers) without model cost.
* **Lane 3: Systems Wedge (`career-fleet recon --lane systems`)**: Evaluates proprietary technical defensibility and infrastructure depth with exact quote citations.
* **Lane 4: Culture Recon (`career-fleet recon --lane culture`)**: Assesses founder pedigree, technical humility, and team distribution (timezone sync friction).
