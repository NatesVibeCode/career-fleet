# Career Fleet troubleshooting

Use this reference when onboarding or operating commands do not behave as expected.

## The command is not found

Activate the virtual environment and install the project:

```bash
. .venv/bin/activate
python3 -m pip install -e ".[dev,discover]"
career-fleet --version
```

If the shell has no `python` command, use `python3` on macOS/Linux or `py` on Windows. From the checkout, `python3 -m career_fleet.cli ...` is a useful diagnostic fallback.

## Discovery fails

The command now exits nonzero and prints the source error. Check:

- `yc` targets are batches such as `W24` or `S24`, or a search phrase.
- ATS targets are public board or organization tokens, not full job URLs.
- `site` targets are absolute `http://` or `https://` URLs.
- `--max` is at least 1.
- The optional discovery dependencies are installed when site extraction needs them.

An error result must not be treated as a completed discovery run. Recheck `career-fleet list --db career_fleet.db` for any partial records before retrying.

## Triage removes more or fewer companies than expected

Inspect the active profile with `career-fleet profile --path profile.json`. The generated profile is neutral; it does not assume remote work, a headcount limit, or AI-wrapper/quota exclusions.

Under `remote_only`, the captured source must show remote evidence. A missing or contradictory location is rejected as unverified. Add source timezone metadata before enabling `candidate_timezone` checks.

Use `dossier --company <id>` to see the posting text and the exact rule quote that caused a rejection.

## Recon evaluates zero companies

The lanes are ordered:

1. discovery creates `discovered` records;
2. triage moves survivors to `triaged`;
3. systems recon evaluates triaged survivors;
4. culture recon evaluates only systems-passing survivors.

Run `recon --lane all` after triage. Running culture alone before a systems pass is expected to evaluate zero companies.

## A profile was overwritten or will not load

`profile --init` refuses to overwrite an existing profile. Use `profile --init --force` only to reset it. If a custom `--profile` path is missing or invalid, the command exits with a clear error instead of silently using defaults.

## The assistant skill is missing

From the workspace root, run:

```bash
career-fleet setup --workspace-root .
```

If a different managed skill already exists there, inspect it first. Use `--force` only when replacing it is intentional.
