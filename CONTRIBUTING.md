# Contributing to career-fleet

Thank you for your interest in contributing to `career-fleet`!

## Philosophy
`career-fleet` keeps its trust boundaries explicit:
1. SQLite owns captured companies, postings, evaluations, and the current funnel status.
2. Discovery keeps source text verbatim so evaluation quotes remain checkable.
3. Deterministic dealbreakers run before technical and culture scoring.
4. A company is exported as qualified only after both recon lanes pass.
5. Source adapters must be covered by end-to-end tests, not only isolated parser tests.

## Development Setup

```bash
git clone https://github.com/NatesVibeCode/career-fleet.git
cd career-fleet

# Install dependencies in editable mode, including discovery adapters
python3 -m pip install -e ".[dev,discover]"

# Install the assistant skill into a workspace when needed
career-fleet setup --workspace-root .

# Run test suite
python3 -m pytest -v

# Check the shared free-fleet contract across the local sibling checkouts
python3 scripts/check_fleet_drift.py
```

Do not include credentials, private candidate data, provider responses containing private data, or local machine paths in issues, fixtures, or commits.

When changing a lane, add a test that exercises the store-backed path as well as any pure scoring helper.
