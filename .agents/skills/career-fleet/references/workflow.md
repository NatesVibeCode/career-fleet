# Career Fleet workflow

Use this reference when operating an initialized workspace.

## Run the funnel

Keep the same database and profile for every command:

```bash
DB=career_fleet.db
PROFILE=profile.json

career-fleet discover --source yc --target W24 --max 30 --db "$DB"
# Or use: greenhouse, ashby, lever, or site with its matching target.
career-fleet triage --db "$DB" --profile "$PROFILE"
career-fleet recon --lane all --db "$DB" --profile "$PROFILE"
career-fleet list --status qualified --db "$DB"
career-fleet export --db "$DB" --output qualified_targets.json

Discovery is refresh-safe: rerunning a source replaces that source's stale
records and lane results while preserving records captured from other sources
for the same company.
```

Source targets are:

- `yc`: a batch such as `W24`, `S24`, or a search phrase.
- `greenhouse`: the public board token, such as `stripe`.
- `ashby`: the public organization token, such as `linear`.
- `lever`: the public organization token.
- `site`: an absolute `http://` or `https://` URL to crawl.

## Understand statuses

- `discovered`: source data is stored but Lane 2 has not run on the current snapshot.
- `triaged`: the company passed configured hard filters and may enter Lane 3.
- `qualified`: Lane 3 passed and Lane 4 reached the healthy threshold.
- `disqualified`: a hard filter rejected the company.

Lane 3 is a gate, not the final decision. Lane 4 only evaluates companies with a passing Lane 3 evaluation and requires a healthy culture score of at least 0.80. A company with an unknown remote-only policy or missing required evidence is not treated as a safe match.

## Rerun safely

Evaluation writes are idempotent: rerunning triage or recon updates the existing lane result instead of creating duplicate rows. Triage rechecks every tracked company, so profile edits take effect; it also clears downstream results that must be recalculated. Re-run `discover` when source postings need refreshing; discovery replaces that company's old postings and lane results before the funnel runs again.

Use `dossier --company <id>` to inspect source URLs, lane verdicts, rationale, and quotes. Add `--show-source` to print the complete captured text before acting on an exported result.

## Tune the profile

Start with a neutral profile, then add only constraints the user actually wants:

```json
{
  "required_stack": ["Python", "PostgreSQL"],
  "negative_stack": ["Legacy Mainframe"],
  "dealbreakers": {
    "max_headcount": 150,
    "policy": "remote_or_hybrid",
    "disallowed_locations": ["Austin"],
    "reject_thin_wrappers": true,
    "reject_pure_quota": true,
    "candidate_timezone": "America/Los_Angeles",
    "min_timezone_overlap_hours": 4
  },
  "target_leadership": ["low-ego technical founders"]
}
```

`candidate_timezone` checks business-hour overlap only when the captured company or posting record also has IANA timezone metadata.

When `max_headcount` is set, an unknown headcount is rejected rather than treated as a match. Each `required_stack` entry must be evidenced for a systems pass; use `/` inside one entry for alternatives such as `Modern Cloud / Kubernetes`.
