# Career-focused community sourcing

Career Fleet can use the shared keyless community adapters as a supplemental
Lane 1 source. These sources are high-recall and noisy, so they are filtered
for career signals before they enter the durable SQLite snapshot.

Supported sources:

- `reddit`: selected-subreddit archive records, or fresh RSS with
  `--reddit-rss` and one or more `--subreddit` values.
- `hn`: Hacker News search followed by full story/thread capture.
- `stackexchange`: full question bodies, optionally with the top answer.
- `discourse`: any Discourse instance, using `--target` as the instance and
  `--query` as the search phrase.
- `lobsters`: a tag in `--target`, or `newest` with an optional `--query`.
- `lemmy`: full post/comment search on the configured instance.
- `devto`: a tag in `--target`, with full article bodies when available.

Examples:

```bash
DB=career_fleet.db
PROFILE=profile.json

career-fleet discover --source reddit --target "hiring platform engineers" \
  --subreddit startups --profile "$PROFILE" --db "$DB"
career-fleet discover --source hn --target "who is hiring distributed systems" \
  --profile "$PROFILE" --db "$DB"
career-fleet discover --source discourse --target https://discuss.python.org \
  --query hiring --profile "$PROFILE" --db "$DB"
career-fleet discover --source devto --target career \
  --profile "$PROFILE" --db "$DB"
career-fleet signals --unlinked --db "$DB"
```

`career-fleet init` creates `career_sources.json` with one deterministic,
career-focused preset for each source. The normal path is:

```bash
career-fleet sources
career-fleet discover --source community --db "$DB"
```

The plan contains the exact public communities, tags, instances, queries, and
10-item default for each source. Edit the file when a user wants a different
source mix. `--preset <id>` selects one configured entry; explicit flags remain
available for one-off overrides.

The default career-focus filter looks for hiring, role, workplace,
compensation, or leadership signals. Profile stack, wedge, catalyst, and
leadership phrases increase the lead score but never manufacture a company
fit claim. A community record is attached to the company funnel only when it
contains exactly one external company domain; otherwise it is retained as an
unlinked lead for human attribution. Use `--include-low-signal` when auditing
the raw source capture.

After discovery, run the normal funnel with the same database and profile:

```bash
career-fleet triage --db "$DB" --profile "$PROFILE"
career-fleet recon --lane all --db "$DB" --profile "$PROFILE"
career-fleet dossier --company <company-id> --db "$DB"
```
