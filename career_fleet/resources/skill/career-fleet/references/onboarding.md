# Career Fleet onboarding

Use this reference for a new user or a new machine.

## Install

Career Fleet requires Python 3.10 or newer. From a clone:

```bash
python3 -m venv .venv
. .venv/bin/activate
python3 -m pip install -e ".[dev,discover]"
career-fleet --version
```

On Windows, create the environment with `py -m venv .venv`, activate `.venv\Scripts\activate`, and use `py -m pip`.

The `discover` extra enables broad web-page extraction and search helpers. YC and the structured ATS APIs work with the base package.

## Initialize a workspace

Run these commands from the workspace that should own the local data:

```bash
career-fleet init
career-fleet setup --workspace-root .
career-fleet profile
```

This creates `profile.json`, `career_fleet.db`, and `.agents/skills/career-fleet/`. The generated profile is neutral. Edit `profile.json` before screening anyone's opportunities.

`profile.json` is the human-editable authoring file. `career-fleet init`,
`career-fleet profile`, and the screening lanes persist the active IEP in
SQLite as an immutable revision; each evaluation records that revision. If the
implicit `profile.json` is unavailable later, triage and recon can recover the
active IEP from the selected database. An explicitly supplied missing
`--profile PATH` still fails, so a typo is never silently ignored.

Useful profile fields include:

- `required_stack`, `negative_stack`, and `wedge_capabilities` for fit signals. Every `required_stack` entry must be evidenced for a systems pass; `/` separates alternatives within one entry.
- `dealbreakers.max_headcount` and `disallowed_locations` for hard filters. Set
  `dealbreakers.require_verified_headcount` to `true` only when missing or
  non-numeric headcount should disqualify a company; it defaults to `false`
  because ATS boards often omit headcount.
- `dealbreakers.reject_thin_wrappers` and `reject_pure_quota` for domain-specific exclusions.
- `dealbreakers.candidate_timezone` plus posting timezone metadata when overlap matters.

### Workplace preference is optional

Do not assume that every user wants remote work. The generated profile uses
`"policy": "any"`, which applies no remote or hybrid requirement. During
onboarding, ask the user to choose only if this matters:

- `any`: accept remote, hybrid, or in-person work unless another configured rule rejects it.
- `remote_only`: require verifiable remote evidence for the role.
- `remote_or_hybrid`: require verifiable remote or hybrid evidence for the role.

Leave the value as `any` when the user has no stated workplace preference.

`career-fleet profile --init` refuses to overwrite an existing profile. Use `--force` only when resetting it intentionally.

## Verify the install

```bash
career-fleet --help
career-fleet profile
test -f career_fleet.db
test -f profile.json
test -f .agents/skills/career-fleet/SKILL.md
```

In PowerShell, use `Test-Path` in place of `test -f`.

Keep the database and profile local. They may contain source-derived or private career data and are ignored by the repository defaults.
