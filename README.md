# career-fleet

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Python 3.10+](https://img.shields.io/badge/python-3.10%2B-blue.svg)](pyproject.toml)

> **Autonomous career & employer intelligence engine with multi-lane screening, exact quote verification, and zero-cost model execution.**

`career-fleet` turns a builder, engineer, or operator's background and career non-negotiables into a systematically qualified pipeline of target companies and high-leverage opportunities. Every qualification is backed by verbatim source quotes from job postings, engineering blogs, and company architecture documents.

*Canonical CLI is `career-fleet` (`career-lanes` remains available as an alias).*

---

## The 4-Lane Funnel Architecture

```
0. Autonomous IEP Calibration ──> Lane 1: Sourcing (YC, VC, ATS, Sitemaps)
                                                      │
                                                      ▼
Lane 4: Founder & Culture Recon      <── Lane 3: Systems Wedge <── Lane 2: Gatekeeper Triage
          │                                                               │
          ▼                                                               ▼
   Qualified Targets                                             Dropped Dealbreakers
```

1. **Lane 1: Sourcing & Discovery**: Ingests companies from YC batches, VC seed portfolios, and direct ATS boards (Ashby, Greenhouse, Lever) using polite crawling and deterministic IDs.
2. **Lane 2: Gatekeeper Triage**: Fast deterministic screening against hard dealbreakers (headcount limits, mandatory non-local office mandates, shallow prompt wrappers) without token cost.
3. **Lane 3: Systems Wedge**: Technical architecture depth and moat evaluation (distributed systems, state machines, compliance rails vs. commodity wrappers).
4. **Lane 4: Founder & Culture Recon**: Evaluates leadership caliber, technical humility, and team distribution (timezone sync friction).

---

## 30-Second Quickstart

### 1. Initialize your workspace and profile

```bash
# Initialize local SQLite database and default Ideal Employer Profile (profile.json)
career-fleet init

# Inspect or calibrate your profile criteria
career-fleet profile
```

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

# Inspect full dossier with exact supporting quotes
career-fleet dossier --company stripe

# Export qualified dossiers to JSON
career-fleet export --output qualified_targets.json
```

---

## Clean Architecture: The Twin-Sister Fork

`career-fleet` is architected as a **namespace-isolated fork** of `bulk-lanes` (`free-fleet`):
* `free_fleet/`: Untouched shared engine layer (Bayesian route scoring, 429 adaptive backoff, mechanical discovery).
* `career_fleet/`: Career-specific domain logic, profile models, and 4-lane pipeline.
* `.agents/skills/career-fleet/`: Assistant skill for Claude, Antigravity, and Cursor.

### Syncing Upstream Improvements

Because `career_fleet/` lives in an isolated namespace, pulling new scrapers and scoring features from upstream `bulk-lanes` is 100% conflict-free:

```bash
git fetch upstream-engine
git merge upstream-engine/master
```

---

## License

MIT License. See [LICENSE](LICENSE).
