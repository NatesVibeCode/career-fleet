# career-fleet

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Python 3.10+](https://img.shields.io/badge/python-3.10%2B-blue.svg)](pyproject.toml)

> **Deterministic career & employer screening engine with multi-lane checks and verbatim source quotes.**

`career-fleet` turns a candidate's stated career criteria into a systematically screened pipeline of target companies and opportunities. Its built-in lanes use explicit rules and keyword signals, not nuanced model judgment. Every qualification is backed by verbatim source quotes from captured source records.

*Canonical CLI is `career-fleet` (`career-lanes` remains available as an alias).*

---

## Install

```bash
python3 -m pip install -e ".[dev,discover]"

# Install the bundled assistant skill into this workspace when needed
career-fleet setup --workspace-root .
```

On Windows, use `py -m pip` in place of `python3 -m pip`.

---

## The 4-Lane Funnel Architecture

```
0. Authorized IEP Calibration ──> Lane 1: Sourcing (YC, ATS, site crawl)
                                                      │
                                                      ▼
Lane 4: Founder & Culture Recon      <── Lane 3: Systems Wedge <── Lane 2: Gatekeeper Triage
          │                                                               │
          ▼                                                               ▼
   Qualified Targets                                             Dropped Dealbreakers
```

1. **Lane 1: Sourcing & Discovery**: Ingests companies from YC batches, direct ATS boards (Ashby, Greenhouse, Lever), or a same-origin site crawl using polite crawling and deterministic IDs.
2. **Lane 2: Gatekeeper Triage**: Fast deterministic screening against hard dealbreakers (headcount limits, mandatory non-local office mandates, shallow prompt wrappers) without token cost.
3. **Lane 3: Systems Wedge**: Technical architecture depth and moat evaluation (distributed systems, state machines, compliance rails vs. commodity wrappers).
4. **Lane 4: Founder & Culture Recon**: Evaluates leadership and communication signals found in captured source text.

The result is a shortlist for human review, not a hiring decision. Inspect the source quotes and current posting URLs before acting on a result.

---

## 30-Second Quickstart

### 1. Initialize your workspace and profile

```bash
# Initialize local SQLite database and default Ideal Employer Profile (profile.json)
career-fleet init

# Inspect or calibrate your profile criteria
career-fleet profile
```

The generated profile is intentionally neutral. Edit `profile.json` to set your stack, dealbreakers, leadership signals, and (optionally) `dealbreakers.candidate_timezone` before relying on qualification results. Workplace screening is also opt-in: `dealbreakers.policy` defaults to `any`, so the lanes do not assume remote or hybrid work. Choose `remote_only` or `remote_or_hybrid` only when the user explicitly wants that filter. If a profile is missing, triage and recon stop with an error instead of silently using neutral criteria.

`profile.json` is the editable authoring file; the selected IEP is also stored in
SQLite as an immutable profile revision, and each lane evaluation records the
revision it used. Account research uses the same pattern with
`ideal_company_profile.json` and `account-fleet profile`.

### 2. Discover target companies & job postings (Lane 1)

```bash
# Discover YC startups from a recent batch
career-fleet discover --source yc --target W24 --max 30

# Or ingest an ATS job board directly
career-fleet discover --source greenhouse --target stripe
career-fleet discover --source ashby --target linear
```

### 3. Filter dealbreakers (Lane 2)

```bash
# Drops headcount bloat, SF/NYC office mandates, and thin wrappers instantly
career-fleet triage
```

### 4. Evaluate technical wedge & culture (Lanes 3 & 4)

```bash
# Evaluates defensibility, moat, and leadership culture
career-fleet recon --lane all
```

### 5. Inspect qualified dossiers & export

```bash
# List qualified survivor companies
career-fleet list --status qualified

# Inspect a dossier with source URLs and exact supporting quotes
career-fleet dossier --company stripe

# Include the complete captured source text when needed
career-fleet dossier --company stripe --show-source

# Export qualified dossiers to JSON
career-fleet export --output qualified_targets.json
```

---

## Clean Architecture: The Twin-Sister Fork

`career-fleet` is architected as a **namespace-isolated fork** of `bulk-lanes` (`free-fleet`):
* `free_fleet/`: Shared engine layer (Bayesian route scoring, 429 adaptive backoff, mechanical discovery).
* `career_fleet/`: Career-specific domain logic, profile models, and 4-lane pipeline.
* `.agents/skills/career-fleet/`: Assistant skill for Claude, Antigravity, and Cursor.

### Syncing Upstream Improvements

Because `career_fleet/` lives in an isolated namespace, pulling new scrapers and scoring features from upstream `bulk-lanes` is conflict-free at the Python namespace level:

```bash
git fetch upstream-engine
git merge upstream-engine/master
```

---

## License

MIT License. See [LICENSE](LICENSE).
