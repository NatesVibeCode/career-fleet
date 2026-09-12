# Discovery Playbook: Finding 500–1,000 Accounts for Free

This playbook guides an AI agent on how to discover high-intent target accounts from public sources without using paid data vendors.

---

## 1. ATS Search Operators

Applicant Tracking Systems are public goldmines. When a company posts a job opening on Ashby or Greenhouse, they state the exact tech stack they run and the exact architectural challenges they need to solve.

### Ashby (`jobs.ashbyhq.com`)
Dominant among modern AI, DevTools, and high-growth B2B SaaS startups (e.g. OpenAI, Linear, Ramp, Supabase, Perplexity).

```text
site:jobs.ashbyhq.com "[TechStack]" ("[PainPoint1]" OR "[PainPoint2]")
```

*Examples:*
- `site:jobs.ashbyhq.com "Kafka" ("billing" OR "streaming" OR "migration")`
- `site:jobs.ashbyhq.com "ClickHouse" "scaling"`
- `site:jobs.ashbyhq.com "Vector" ("Pinecone" OR "Qdrant" OR "latency")`

### Greenhouse (`boards.greenhouse.io`)
Used by established venture-backed scaleups and mid-market enterprise tech (e.g. Stripe, Figma, Databricks).

```text
site:boards.greenhouse.io "[TechStack]" ("Staff Engineer" OR "Infrastructure")
```

*Examples:*
- `site:boards.greenhouse.io "Postgres" ("50k QPS" OR "connection pool")`
- `site:boards.greenhouse.io "Kubernetes" ("multi-cluster" OR "cost optimization")`

### Lever (`jobs.lever.co`)
Used by global scaleups and remote engineering organizations.

```text
site:jobs.lever.co "[TechStack]" ("Senior Backend" OR "Platform")
```

---

## 2. Compiling the Discovered Raw Accounts into CSV

When the agent uses web search or web scraping tools, format each found job posting into an `accounts.csv` with these exact columns:

```csv
item_id,text,source_uri
stripe.com,"We are looking for a Staff Engineer to join our Platform team. In this role, you will be responsible for leading the migration of our legacy billing service to Apache Kafka and modernizing our distributed streaming architecture. Requirements: 8+ years experience with distributed event streaming...","https://jobs.ashbyhq.com/stripe/staff-infrastructure-engineer"
hyper_ai,"Looking for Senior Backend Engineer hitting latency limits at 50k QPS on Postgres cluster. You will optimize query plans, implement Read Replicas, and introduce caching layers...","https://boards.greenhouse.io/hyperai/backend-scale"
supabase.com,"Building globally distributed Edge Functions replication with low-latency state sync. Ideal candidates have deep expertise with WebAssembly and distributed consensus...","https://jobs.lever.co/supabase/edge-eng"
```

### Ingestion Requirements:
1. `item_id`: Use clean, canonical company domains (e.g. `stripe.com`, `hyper_ai`) so deduplication is automatic.
2. `text`: Preserve the raw paragraph or sentence where the bottleneck is discussed. Do not summarize or alter the text—the model must quote it verbatim during qualification.
3. `source_uri`: Always include the full URL of the post so that sales reps can click through to verify the opening.

---

## 3. Alternative Zero-Cost Discovery Sources

If ATS searches don't yield enough candidates, query these free registries:

### Hacker News "Who is Hiring" (Monthly Dump)
- Query: `site:news.ycombinator.com "Ask HN: Who is hiring?" "[Month Year]"`
- Contains 500–800 tech companies hiring every month. Each comment contains company name, URL, tech stack, and hiring manager email.

### Y Combinator Company Directory
- Endpoint: `https://api.ycombinator.com/v0.1/companies`
- Free public API containing ~5,000 top startups, filterable by batch (`W24`, `S23`), tags (`B2B`, `DevTools`, `Fintech`), and team size.

---

## 4. Mechanical Discovery (CLI; Beyond Job Boards)

Hand-run `site:` queries are the fallback, not the default. The CLI searches the
broad web, fetches hits politely (robots.txt, rate delay, size caps), parses
pages to verbatim text, and writes `accounts.csv` ready for `run`/`export`:

```bash
# Broad web search across ddgs metasearch + HN Algolia (no API keys)
account-fleet discover \
  --query '"Kafka" ("migration" OR "billing") hiring' \
  --query '"Postgres" ("latency" OR "50k QPS") hiring' \
  --max-results 20 --output accounts.csv

# Self-hosted SearXNG instead of ddgs (requires `pip install account-fleet[discover]` for ddgs;
# SearXNG itself needs no extra package, just a running instance)
account-fleet discover --query '"ClickHouse" scaling hiring' \
  --backend searxng --searxng-url http://localhost:8888 --output accounts.csv

# Structured ATS intake via keyless JSON APIs (no search needed)
account-fleet fetch --greenhouse-board stripe --max-jobs 50 --output accounts.csv
account-fleet fetch --ashby-org linear --output accounts.csv

# YC company directory: keyword/batch/tag search over ~6k startups (indicator-grade profiles)
account-fleet discover --query "devtools" --backend yc --max-results 20 --output accounts.csv
account-fleet fetch --yc --yc-batch W24 --yc-tag B2B --max-jobs 50 --output accounts.csv

# Community talk: Reddit search + fresh posts, full HN threads (indicator-grade)
account-fleet discover --query "kafka latency" --backend reddit --subreddit dataengineering --output accounts.csv
account-fleet fetch --subreddit dataengineering --subreddit-sort new --max-jobs 25 --output accounts.csv
account-fleet fetch --hn 8863 --max-comments 50 --output accounts.csv

# Q&A + forums (all keyless; at least one flag is required per source)
account-fleet discover --query "kafka consumer lag" --backend stackexchange --se-tagged apache-kafka --output accounts.csv
account-fleet fetch --stackexchange-query "kafka consumer lag" --se-tag apache-kafka --se-answers --output accounts.csv
account-fleet fetch --discourse discuss.kubernetes.io --discourse-query "kafka" --max-jobs 10 --output accounts.csv
account-fleet fetch --lobsters-tag databases --max-jobs 25 --output accounts.csv
account-fleet fetch --lemmy-query "kafka" --max-jobs 25 --output accounts.csv
account-fleet fetch --devto-tag kafka --max-jobs 10 --output accounts.csv

# Whole docs sites: sitemap first, same-origin crawl fallback (depth-capped, robots-aware)
account-fleet fetch --sitemap https://docs.example.com/sitemap.xml --max-jobs 30 --output accounts.csv
account-fleet fetch --site docs.example.com --max-pages 20 --max-depth 2 --output accounts.csv

# Arbitrary URLs (engineering blogs, docs, changelogs, PDFs) to verbatim text
account-fleet fetch --url https://example.com/blog/scaling-postgres --output accounts.csv

# JS-heavy pages (experimental; needs `pip install account-fleet[js]`)
account-fleet fetch --url https://example.com/app --js --output accounts.csv
```

Article parsing prefers `trafilatura`, then `readability-lxml`, then a stdlib
fallback that skips nav/footer/chat chrome (all three in the `discover`
extra, which also adds `pypdf` for PDF URLs). Fetching is polite by default:
robots.txt honored (Allow/Disallow longest-match), 1s delay between fetches,
2MB per-page cap. Fetched `text` is stored verbatim so
exact-offset quote verification keeps working unchanged.

Every record carries `metadata.evidence`: `fetched` (full page text,
grounding-grade), `profile` (YC/ATS structured text, indicator), `indicator`
(search snippet, triage only). **Snippets and profiles are indicators, never
proof: tier-1 claims must cite full fetched text.** `--snippets-only` is a
cheap triage pass; re-run without it before scoring.

Community sources (Reddit, HN threads) are high-recall but noisy: expect
vendor self-promo and single-author opinions. Treat one post as a lead;
treat five engineers across threads independently reporting the same pain as
an account signal. Rate limits bite here (Reddit RSS 429s within a handful
of requests; Arctic Shift throttles full-text search): the CLI backs off
automatically, and `--delay` buys politeness.

Q&A sources (Stack Exchange, Discourse forums, Dev.to, Lobsters, Lemmy) skew
toward practitioners describing real constraints; treat vote counts only as
triage. Stack Exchange allows 300 anonymous calls/day: `--se-answers` spends
one extra call per question, so leave it off for wide pulls. `--backend
lobsters`/`--backend devto` filter the newest listing client-side (these APIs
have no search endpoint), so old threads need `--lobsters-tag`/RSS-style
sources instead. For Discourse, pass any instance URL
(`--discourse community.example.com`) — the same flags work against hundreds
of self-hosted forums.
