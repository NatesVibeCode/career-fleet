# Career Fleet

Career Fleet researches companies from an explicit objective or supplied collection. Its deterministic discovery Process searches selected sources, preserves their responses, finds words and declared related concepts, and collects evidence for requested questions. Sparse and unresolved companies remain visible.

## Install and build

Python 3.10+ and Go 1.26+ are required to build from source:

```sh
python3 -m pip install -e .
career-fleet build
```

The build compiles the bundled standalone worker locally with module downloads disabled. Executions need neither the Go compiler nor Forge, Work, Engine, an inference provider, or their services. Browser-backed sources also need a caller-supplied local Node, Playwright and Chromium runtime binding.

## Start with a request

```sh
career-fleet init --objective "ceramic filtration suppliers" --pages 3 --output research-request.json
```

`init` includes 40 public source configurations plus twelve Forge-published source Blocks across web search, news, startup and software directories, marketplaces, developer catalogs, and community search. It includes Google/Bing/DuckDuckGo search, Hacker News, GitHub, GitLab, npm, Stack Exchange, DEV, Reddit, Discourse, Lemmy, Lobsters, and company/product directories. Search queries start from the objective and deterministic generic synonyms; fixed source Blocks enumerate their declared roster rather than filtering it by the objective. Source choice remains explicit. They do not encode a personal profile or fit checklist. The template leaves criteria empty and acquisition disabled.

Review the source list in the request, then enable acquisition when ready. `--pages` selects how far paginated APIs and search pages are read; feeds without pagination still contribute their first response. There is no hidden request ceiling. Set an explicit optional `--max-requests` allowance if you want one:

```sh
career-fleet init --objective "ceramic filtration suppliers" --pages 3 --enable-acquisition --max-requests 150 --output research-request.json
```

Use `--empty-sources` to opt out of the catalog. Discovery does not load `profile.json` or a stored personal profile. Each attempted source query, exact URL, response, parser coverage and failure is retained in the execution evidence.

HTML search adapters read static HTML and extract linked results. They record a coverage gap because static fetches cannot guarantee JavaScript-rendered results. Use `--enable-browser` to select the Process's pinned browser read for Product Hunt, Wellfound, G2, Capterra, Shopify App Store, and Salesforce AppExchange. That request requires `discover --browser-runtime browser-runtime.json`; the JSON contains absolute paths for `node_executable`, `playwright_directory`, `browser_directory`, and `browser_executable`. The Process checks those files against its sealed runtime pins, captures the rendered DOM, and retains the original responses for every browser network read. Browser runs use the caller's remaining `--max-requests` allowance when set.

Product Hunt and Wellfound use explicit typed `browser_mapping` rules. Live checks returned ten Product Hunt product cards and ten Wellfound company-profile cards on page one; Wellfound pagination follows the caller's `--pages` value. G2, Capterra, Shopify App Store, and AppExchange currently returned browser HTTP failures or no listings in the live check, so their acquisition evidence remains marked as incomplete or empty until their routes are repaired.

The closed request schema is bundled at `career_fleet/resources/company_discovery/request.schema.json`. Examples:

- `examples/company-discovery/anonymous-sparse.json`: offline supplied collection, declared equivalent expression, missing comparison data.
- `examples/company-discovery/public-rss-cycle.json`: explicitly selected public RSS search and initial source-document verification. This example permits two requests; that is not a product limit.

```sh
career-fleet discover --request research-request.json --db career_fleet.db
career-fleet list --db career_fleet.db
career-fleet dossier --id 'entity:your-explicit-id' --db career_fleet.db
career-fleet export --db career_fleet.db --output retained-leads.json
```

Discovery prints the actual worker response as JSON. Each execution also retains its exact input, response, evidence artifacts and private journal under `runs/`. Use `--run-directory PATH` to choose a new directory. Keep that directory private; `execution.key` belongs to its journal.

`list` includes every latest retained lead across executions. A failed or empty acquisition does not erase previous results. `dossier` shows every recorded version of an exact lead. Exporting with `--run EVENT_ID` returns that complete execution, including its captures, source failures and coverage.

Any data for any listed business can be requested or refreshed in a new task at any time. Fields are caller-selected through `questions` and extraction rules; they are not assigned to rungs or a required enrichment order. Supply selected businesses as `prior` evidence to continue research. Existing data does not prevent an explicit new request for the same field.

## Source Blocks and discovery Processes

Twelve source Blocks are published through Forge and composed into the discovery Process by exact name and digest. Each Block contains seven Nodes: bind its immutable source definition, capture or reuse, retain bytes, bind the capture, load checked bytes, parse, and inspect. All twelve passed live nonempty extraction, a representative detail read, and refusal of an intentionally absent mapping on the captured listing. This establishes those extraction mechanics; it does not establish exhaustive coverage, relevance to every objective, or globally unique businesses.

| Population | Verified sites | Listing records in the verification run |
| --- | --- | ---: |
| Associations | W3C, Khronos | 308 + 113 |
| Exhibitors | Aquatech Amsterdam, FILTECH | 20 + 601 |
| Suppliers | Made-in-China, IndiaMART ceramic-filter collections | 30 + 28 |
| Procurement awardees | ContractsFinder, Find a Tender | 199 + 6 |
| Geographic directories | San Francisco Chamber, Tacoma Chamber | 51 + 80 |
| Ecosystems | Cloudflare, Vercel | 7 + 3 |

The runs retained 1,439 exact source/registry identities from 1,446 listing records. Those identities have not all been reconciled across sources. Memberships can include non-business organizations. An explicit source website is a source assertion, not independent proof of ownership. Unsupported HTTP links remain visible as sparse records; the Process does not silently upgrade them to HTTPS.

Requests select `source_blocks: [{"block": {"name": "...", "digest": "..."}}]`. The printed opening Node resolves these against the Process's compiled inventory. Source mappings are filled into each Block at build time; callers cannot replace them. Unknown or stale references refuse before acquisition. The `source_digest` on capture metadata is a configuration fingerprint; the Block's native `name` and `digest` identify the reusable capability.

Inspect and verify the actual Block artifacts:

```sh
career-fleet blocks list
career-fleet blocks verify --id exhibitors.aquatech --run-directory runs/verify-aquatech --db research.db
```

Select Block catalog IDs or populations without editing JSON:

```sh
career-fleet sources list --db research.db
career-fleet init --objective "enumerate exhibitors" --source exhibitors.filtech --enable-acquisition --output exhibitors.json
career-fleet campaign run --request exhibitors.json --directory campaigns/exhibitors --db research.db
career-fleet campaign status --directory campaigns/exhibitors
career-fleet campaign resume --directory campaigns/exhibitors --db research.db
career-fleet campaign replay --directory campaigns/exhibitors --db research.db
career-fleet campaign refresh --directory campaigns/exhibitors --db research.db
```

`--source` and `--family` may be repeated; selections are combined. The six new family names are `associations`, `exhibitors`, `suppliers`, `procurement`, `geographic`, and `ecosystems`. `--pages` follows declared next links/cursors. The two geographic examples currently cover the A roster; their returned pages do not expose the alphabet navigation, so no unverified letter traversal is asserted. Repeated pages and confirmed empty responses stop that query. Blocks without a declared next-page mechanism cover their captured listing only. Procurement examples retain an explicit date window or provider page size; change those source parameters deliberately for a different window or roster, then author a new Block revision through Forge, recompose the Process, and verify it.

`resume` continues the same event, pinned worker, input and native journal. `replay` creates a separately labeled event from retained captures with acquisition disabled and the original print pinned. `refresh` creates a new event on the current print, restores the campaign's original acquisition policy, and preserves prior records and lineage. Status reports the last returned Process result; it does not invent live actions for an interrupted worker.

To find additional collections, start from explicitly selected web search channels:

```sh
career-fleet init --objective "industrial businesses" --source web.bing --enable-acquisition --output source-search.json
career-fleet sources discover --request source-search.json --run-directory runs/source-search --db research.db
career-fleet sources inspect --candidate 'source-candidate:EXACT_ID' --preset auto_collection --run-directory runs/verify-source --db research.db
```

The Process searches collection queries across all six populations. Known table, Organization ItemList, declared HTML/embedded JSON, and OCDS supplier shapes can be verified. Unfamiliar structures and blocked pages remain in the retained candidate queue. A candidate JSON can supply `proposed_source.html_mapping`, JSON pointers, readiness selectors and pagination for structure inspection. That proposed input is data. A reusable capability is authored as a Block through Forge and composed into a new Process revision. Runtime source discovery does not publish Blocks.

Original capture bodies are stored once in the run's scoped evidence store and referenced by digest. Parsing loads and checks those exact bytes. Public outputs carry metadata, records and failures rather than repeating large bodies at every Node. Keep the evidence directory with its journal for replay. A damaged artifact refuses to produce a valid result.

## Career Fleet MCP

The canonical MCP exposes the same Process and lifecycle operations as the CLI:

```sh
career-fleet serve --workspace-root /absolute/path/to/research-workspace --db research.db
```

Register that command and those arguments in your MCP client. Tools include `career_fleet_init`, `career_fleet_discover`, `career_fleet_sources_list/discover/inspect`, `career_fleet_blocks_list/verify`, `career_fleet_campaign_run/status/resume/replay/refresh`, `career_fleet_list`, `career_fleet_dossier`, and `career_fleet_export`. Paths stay inside the selected workspace. Process result schemas come from the bundled print. CLI and MCP do not implement a second acquisition loop.

The [implementation evidence report](docs/company-discovery-implementation.md) records the exact print, real-use checks, remaining coverage gaps, and reproducible verification command.

## Research contract

- **Any data, any time:** request any supported field for any selected business, in any order. A new request may refresh an already populated field. Source evidence, missing data and conflicting values remain visible.
- **Task and criteria:** all objectives, preferences, requirements and vocabulary are explicit request data. No personal defaults or default fit weights.
- **Progressive research:** a selected `source_document` question lets an unidentified lead earn an initial source read. Further acquisition follows authored questions and explicit link terms.
- **Identity:** publishers, documents and unresolved mentions are distinct from established company identity. Domains and similar names do not merge companies. Exact document subject bindings are explicit inputs.
- **Evidence:** original binary responses remain separate from derived text. Matches and extracted assertions carry source references and exact text spans. Static HTML and rendered DOM have separate coverage labels; browser runs retain source-response bytes.
- **Conditions:** `equals`, `contains` and `exists` evaluate an exact field and scope. Missing evidence stays unknown; conflicting claims remain visible. Unsupported operations refuse explicitly.
- **Scheduling:** cheap retained evidence precedes new reads; selected acquisition rotates eligible leads. No age, publicity or documentation-volume advantage.
- **Comparison:** stable discovery order or explicitly selected retrieval relevance; requested fields remain separate comparison rows. There is no composite personal-fit score.
- **Feedback and reuse:** observation-specific retrieval decisions and exact field/scope corrections remain local. Selected captures may be reused without fresh requests; current criteria are recomputed. Feedback creates no inferred personal profile.

The Python CLI builds and launches the worker and records its outputs. The Process owns the flattened typed DAG, Node configuration, mappings, scheduling, recovery and receipts. SQLite stores append-only projections without choosing company identity, scores or research actions.

## Rebuild status

The canonical CLI now uses the native discovery Process. The previous fixed four-lane CLI, automatic profile loading, profile initialization and qualified-only export are superseded. Existing database tables and previously recorded data are retained; new executions use `process_runs` and `process_leads`.

The bundled CLI/MCP runtime is explicitly a **development print**. No inference is configured. Forge now covers the initialization dependency and accepts explicitly selected public GET, Playwright and local capture-store qualification bindings. The separately retained signed release has 22 admitted Node definitions and passed all 18 native AO cases twice, covering 104 required ordinary Nodes. Node admission, release printing and native AO consumption are recorded separately in the [implementation evidence report](docs/company-discovery-implementation.md).

Production Function source and local checks live under `processes/company-discovery/author/core`. The exact printed source and its SHA256 inventory ship with the Python package. HTML tokenizer attribution is retained in the source-selection Node's bundled license file.

## Development checks

```sh
python3 -m pytest tests/career/test_career_sources.py tests/career/test_career_integration.py tests/career/test_career_setup.py tests/career/test_career_campaign.py tests/career/test_career_mcp.py
cd processes/company-discovery/author/core
GOWORK=off go test ./...
```

Historical four-lane implementation modules remain as unregistered source references during the product rebuild. They do not execute through the canonical CLI. The shared `harness_fleet` library retains its own separate contracts and tests.
