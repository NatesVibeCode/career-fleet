# Workflow

1. Build once with `career-fleet build`.
2. Create a request with `career-fleet init --objective "the requested objective" --output request.json`. This seeds the broad generic public source catalog and leaves acquisition disabled.
3. Review its source ids, query patterns, and mappings. Select pagination with `--pages N`; use `--empty-sources` only when the operator asks for no default catalog. `--enable-browser` changes selected JS-heavy marketplaces to the Process's pinned rendered-DOM capture path. Enable acquisition with `--enable-acquisition` and, if useful, set `--max-requests N`. The request schema rejects unknown inputs; no product-wide source quota is baked in.
4. Execute `career-fleet discover --request request.json --db career_fleet.db`. If browser sources are selected, also supply `--browser-runtime browser-runtime.json` with absolute local Node, Playwright and Chromium paths matching the sealed pins.
5. Inspect `career-fleet list --db career_fleet.db` and `career-fleet dossier --id ID --db career_fleet.db`.
6. Export every retained lead with `career-fleet export --db career_fleet.db --output results.json`, or include `--run EVENT_ID` for a complete exact execution.

A question with acquisition `source_document` can verify a sparse unidentified lead. An exact document subject binding is needed before its content becomes attributed company facts. Exact field/scope conditions distinguish support, contradiction, unknown and conflict. Unrequested personal fit stays unassessed. Static HTML and browser-rendered search results keep separate coverage labels; browser captures retain original network response bytes and any denied resources.

The list stays available for arbitrary data requests throughout the work. Select whichever businesses are relevant as `prior`, then request any supported fields through `questions` and rules in any order. A later request can ask for the same field again. Retained evidence may already answer it; preserve its source and age. Explicit campaign refresh creates a fresh execution. Do not assign particular fields to particular rungs or require completing a dossier before requesting another field.

Raw captures, matches, assertions, action dispositions and gaps are returned together. Private run artifacts include the execution journal and its key. Never publish them or a personal research database.


## Collections and persisted work

The catalog includes twelve Forge-published source Blocks with exact content pins and live verification evidence across associations, exhibitors, suppliers, procurement, geographic directories and ecosystems. Inspect `blocks list`; each entry names its actual Block artifact. Select them with repeated `init --source ID` or `--family FAMILY`; fixed rosters are not automatically filtered by the objective. A declared family is a source population, not a fit criterion. Every sparse record remains visible.

Use `campaign run --request request.json --directory campaigns/name --db research.db`, then `campaign status`, `resume`, `replay`, or `refresh` with the same directory. Resume preserves the event and worker. Replay creates a labeled offline execution on the source print. Refresh creates a fresh event on the current print under the original request authority. Keep all evidence files with the journal; raw bodies are retained once and loaded by digest.

`sources discover --request request.json --db research.db` uses selected web search channels to find collection candidates across six populations. Known shapes can pass automatically; blocked or unfamiliar shapes stay queued. `sources inspect --candidate source-candidate:ID --preset auto_collection` checks proposed source data. `blocks verify --id ID` runs an existing pinned Block; source inspection does not publish a Block. A candidate JSON can declare a supported mapping as `proposed_source`; author a reusable Block through Forge before composing it into a Process; do not replace this with an agent-authored runtime scraping loop.

`career-fleet serve --workspace-root /absolute/workspace --db research.db` exposes the same operations over stdio MCP. Use `career_fleet_init`, `career_fleet_discover`, source tools, campaign tools and retained-data tools. Discovery accepts `request_path` and an optional `run_directory`. Headless MCP consumption is tested, while the bundled Process remains an unsigned development print. Separate factory evidence records Node admission, release printing and native AO consumption for exact revisions.

Requests select exact `source_blocks[].block` name/digest references. Each source Block pins the source mapping as an internal Node fill and composes seven acquisition/extraction Nodes. The Process owns phase selection, recurrence and recovery. Changed mappings require new Block and Process revisions; never relabel source configuration data as a Block.
