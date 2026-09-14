# career-fleet

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Python 3.10+](https://img.shields.io/badge/python-3.10%2B-blue.svg)](pyproject.toml)

> **Deterministic career & employer screening engine with multi-lane checks and verbatim source quotes.**

`career-fleet` turns a candidate's stated career criteria into a systematically screened pipeline of target companies and opportunities. Its built-in lanes use explicit rules and keyword signals, not nuanced model judgment. Every qualification is backed by verbatim source quotes from captured source records.

*Canonical CLI is `career-fleet`.*

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
0. Authorized IEP Calibration ──> Lane 1: Sourcing (YC, ATS, site, community)
                                                      │
                                                      ▼
Lane 4: Founder & Culture Recon      <── Lane 3: Systems Wedge <── Lane 2: Gatekeeper Triage
          │                                                               │
          ▼                                                               ▼
   Qualified Targets                                             Dropped Dealbreakers
```

1. **Lane 1: Sourcing & Discovery**: Ingests companies from YC batches, direct ATS boards (Ashby, Greenhouse, Lever), or a same-origin site crawl using polite crawling and deterministic IDs. Career-focused community sources (Reddit, HN, Stack Exchange, Discourse, Lobsters, Lemmy, and Dev.to) are stored as durable signals; only an unambiguous external company domain is promoted into the company funnel.
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

# Add career-focused community evidence. Unlinked leads remain reviewable.
career-fleet discover --source reddit --target "hiring platform engineers" --subreddit startups
career-fleet discover --source hn --target "who is hiring distributed systems"
career-fleet discover --source stackexchange --target "remote engineering jobs" --se-site workplace
career-fleet discover --source discourse --target https://discuss.python.org --query hiring
career-fleet discover --source lobsters --target newest --query hiring
career-fleet discover --source lemmy --target "hiring engineers"
career-fleet discover --source devto --target career
career-fleet signals --unlinked
```

For the standard community pass, `career-fleet init` also writes a visible,
pre-filled `career_sources.json`:

```bash
career-fleet sources
career-fleet discover --source community
```

The source plan contains the exact public communities, tags, instances,
queries, and item limits. Edit it when a user wants a different source mix;
use explicit flags for an advanced one-off override.

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

`career-fleet` is architected as a **namespace-isolated fork** of `harness-fleet` (`harness-fleet`):
* `harness_fleet/`: Shared engine layer (Bayesian route scoring, 429 adaptive backoff, mechanical discovery).
* `career_fleet/`: Career-specific domain logic, profile models, and 4-lane pipeline.
* `.agents/skills/career-fleet/`: Assistant skill for Claude, Antigravity, and Cursor.

### Syncing Upstream Improvements

Because `career_fleet/` lives in an isolated namespace, pulling new scrapers and scoring features from upstream `harness-fleet` is conflict-free at the Python namespace level:

```bash
git fetch upstream-engine
git merge upstream-engine/master
```

---

## License

MIT License. See [LICENSE](LICENSE).

## Migrating from free-fleet

Version 0.3.0 renames the `free-fleet` distribution to `harness-fleet` (the old `bulk-lanes` name is gone). Back up your database first, then:

```bash
free-fleet db backup free-fleet.db.bak  # back up with the OLD CLI first (SQLite backup API, WAL-safe)
mv free-fleet.db harness-fleet.db
career-fleet setup --workspace-root .   # re-installs the skill
career-fleet mcp install                # re-installs client configs
```

Old packets (`free_fleet_v2` / `bulk_lanes_v2`) no longer read; re-export them from SQLite before upgrading. The `FREE_FLEET_DB` / `BULK_LANES_DB` / `ACCOUNT_FLEET_DB` variables are replaced by the single `HARNESS_FLEET_DB`. There is no downgrade path — restore your backup to go back.
