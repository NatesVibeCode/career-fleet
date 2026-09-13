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

Useful profile fields include:

- `required_stack`, `negative_stack`, and `wedge_capabilities` for fit signals.
- `dealbreakers.max_headcount`, `policy`, and `disallowed_locations` for hard filters.
- `dealbreakers.reject_thin_wrappers` and `reject_pure_quota` for domain-specific exclusions.
- `dealbreakers.candidate_timezone` plus posting timezone metadata when overlap matters.

`career-fleet profile --init` refuses to overwrite an existing profile. Use `--force` only when resetting it intentionally.

## Verify the install

```bash
career-fleet --help
career-fleet profile
test -f career_fleet.db
test -f profile.json
test -f .agents/skills/career-fleet/SKILL.md
```

Keep the database and profile local. They may contain source-derived or private career data and are ignored by the repository defaults.
