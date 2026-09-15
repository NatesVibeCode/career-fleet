# career-fleet

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Python 3.10+](https://img.shields.io/badge/python-3.10%2B-blue.svg)](pyproject.toml)

> **Deterministic career & employer screening engine with multi-lane checks and verbatim source quotes.**

Career Fleet turns what you want from your next job into a screened shortlist of companies and openings. It rules out the obvious mismatches first, then reads the survivors for the things that actually matter — the problem they need solved, the stack, why they are hiring now, and who leads the team. Every judgement quotes the posting it came from, so you can check it yourself, and the built-in rules stay predictable rather than guessing.

*Canonical CLI is `career-fleet`.*

## Start here (no coding needed)

1. **Install.** macOS/Linux: `./install.sh` · Windows: `powershell -ExecutionPolicy Bypass -File install.ps1`
   It makes its own private Python environment, installs the skills, creates your `profile.json`, and runs the demo on sample postings.

   Prefer a terminal one-liner to the installer? This puts the CLI on your PATH without cloning anything:
   ```bash
   uv tool install "git+https://github.com/NatesVibeCode/career-fleet"
   # or, without uv:  python3 -m pip install "git+https://github.com/NatesVibeCode/career-fleet"
   ```
   Working on this repository instead of using it? `python3 -m pip install -e ".[dev,discover]"`.
2. **Set up and run:** `career-fleet init` creates your database and a plain-language `profile.json` describing what you want. Then `career-fleet board --open` opens a local web page showing your pipeline — no commands needed to read results.
3. **Ask your assistant.** Restart Claude Desktop (or Cursor) and describe the job you want in plain words; the bundled `career-fleet` skill runs the lanes and explains its reasoning with quotes from the postings.

Career Fleet's own lanes need no AI account at all — screening runs on rules and the text of the postings. If you also want the shared engine's batch runs, [FREE-ACCESS.md](FREE-ACCESS.md) covers the free options.

## Which fleet do I want?

Every distribution in this family shares one engine — typed claims, SQLite checkpoints, and character-exact quote verification — and ships assistant skills alongside it. Install one per environment.

| If you want to… | Install | CLI | Skill that drives it |
| --- | --- | --- | --- |
| Find and rank **employers and job postings** | **career-fleet** ← you are here | `career-fleet` | `career-fleet` |
| Turn an ICP into **scored target accounts** | [account-fleet](https://github.com/NatesVibeCode/account-fleet) | `account-fleet` | `account-fleet` |
| Score, classify, extract, or triage **your own** text at volume | [harness-fleet](https://github.com/NatesVibeCode/harness-fleet) | `harness-fleet` | `harness-fleet` |
| Find **implementation partners and SIs** | harness-fleet, preset `partner-research` | `harness-fleet` | `partner-fleet` |

**New here?** [Install](#install) → [30-second quickstart](#30-second-quickstart) → [jobs board](#6-interactive-jobs-board).

---

## Install

```bash
python3 -m pip install -e ".[dev,discover]"

# Install the bundled assistant skill into this workspace when needed
career-fleet setup --workspace-root .
```

On Windows, use `py -m pip` in place of `python3 -m pip`.

---

## Assistant skills (what installs where)

Skills are the playbooks your AI client reads to drive this CLI. `career-fleet setup` installs every skill this distribution bundles into `<workspace>/.agents/skills/`:

| Skill | Installed as | Drives |
| --- | --- | --- |
| `career-fleet` | `.agents/skills/career-fleet/` | This CLI: onboarding, the 4-lane workflow, career signals, community sourcing, scoring rubric, troubleshooting |
| `harness-fleet` | `.agents/skills/harness-fleet/` | The shared engine underneath: task contracts, runs, export, MCP |

Career Fleet and Account Fleet are separate products: this distribution ships neither Account Fleet's skill nor its examples.

Each skill is plain markdown — a `SKILL.md` plus a `references/` folder. Read them straight from this repo under `skills/`, or preview what setup would install:

```bash
career-fleet setup --workspace-root "$PWD" --dry-run --json
```

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
revision it used.

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

### 6. Interactive Jobs Board

```bash
# Launch the interactive 3-column dashboard UI at http://127.0.0.1:8000
career-fleet board

# Automatically open the browser or specify a database
career-fleet board --open
career-fleet board --db ../career-public-research-worker/career_research.db --port 8080
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
