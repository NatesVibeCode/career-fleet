# Career Fleet

Career Fleet researches companies from an explicit objective or supplied collection. Its deterministic discovery Process searches selected sources, preserves their responses, finds words and declared related concepts, and collects evidence for requested questions. Sparse and unresolved companies remain visible.

## Install and build

Python 3.10+ and Go 1.26+ are required to build from source:

```sh
python3 -m pip install -e .
career-fleet build
```

The build compiles the bundled standalone worker locally with module downloads disabled. After building, executions need neither the Go compiler nor Forge, Work, Engine, an inference provider, or their services.

## Start with a request

```sh
career-fleet init --objective "ceramic filtration suppliers" --output research-request.json
```

Edit the request to select vocabulary, sources, questions, criteria and acquisition policy. The initial template has empty criteria and acquisition disabled. Discovery does not load `profile.json` or a stored personal profile.

The closed request schema is bundled at `career_fleet/resources/company_discovery/request.schema.json`. Examples:

- `examples/company-discovery/anonymous-sparse.json`: offline supplied collection, declared equivalent expression, missing comparison data.
- `examples/company-discovery/public-rss-cycle.json`: explicitly selected public RSS search and initial source-document verification. This example permits two requests; that is not a product limit.

```sh
career-fleet discover --request research-request.json --db career_fleet.db
career-fleet list --db career_fleet.db
career-fleet dossier --id 'entity:your-explicit-id' --db career_fleet.db
career-fleet export --db career_fleet.db --output retained-leads.json
```

Discovery prints the actual worker response as JSON. Each execution also retains its exact input, response, diagnostics and private journal under `runs/`. Use `--run-directory PATH` to choose a new directory. Keep that directory private; `execution.key` belongs to its journal.

`list` includes every latest retained lead across executions. A failed or empty acquisition does not erase previous results. `dossier` shows every recorded version of an exact lead. Exporting with `--run EVENT_ID` returns that complete execution, including its captures, source failures and coverage.

## Research contract

- **Task and criteria:** all objectives, preferences, requirements and vocabulary are explicit request data. No personal defaults or default fit weights.
- **Progressive research:** a selected `source_document` question lets an unidentified lead earn an initial source read. Further acquisition follows authored questions and explicit link terms.
- **Identity:** publishers, documents and unresolved mentions are distinct from established company identity. Domains and similar names do not merge companies. Exact document subject bindings are explicit inputs.
- **Evidence:** original binary responses remain separate from derived text. Matches and extracted assertions carry source references and exact text spans. Static HTML does not imply browser-rendered coverage.
- **Conditions:** `equals`, `contains` and `exists` evaluate an exact field and scope. Missing evidence stays unknown; conflicting claims remain visible. Unsupported operations refuse explicitly.
- **Scheduling:** cheap retained evidence precedes new reads; selected acquisition rotates eligible leads. No age, publicity or documentation-volume advantage.
- **Comparison:** stable discovery order or explicitly selected retrieval relevance; requested fields remain separate comparison rows. There is no composite personal-fit score.
- **Feedback and reuse:** observation-specific retrieval decisions and exact field/scope corrections remain local. Selected captures may be reused without fresh requests; current criteria are recomputed. Feedback creates no inferred personal profile.

The Python CLI builds and launches the worker and records its outputs. The Process owns the flattened typed DAG, Node configuration, mappings, scheduling, recovery and receipts. SQLite stores append-only projections without choosing company identity, scores or research actions.

## Rebuild status

The canonical CLI now uses the native discovery Process. The previous fixed four-lane CLI, automatic profile loading, profile initialization and qualified-only export are superseded. Existing database tables and previously recorded data are retained; new executions use `process_runs` and `process_leads`.

This is a **development print**, not an admitted release. No inference is configured. Optional semantic assessment and native AO admission remain unfinished. A Forge outcome-coverage validator currently omits the Process's initialization dependency; that qualification gap must be repaired through Forge before admission. The CLI does not bypass the gate or fall back to the old discovery loop.

Production Function source and local checks live under `processes/company-discovery/author/core`. The exact printed source and its SHA256 inventory ship with the Python package. HTML tokenizer attribution is retained in the bundled licenses directory.

## Development checks

```sh
python3 -m pytest tests/career/test_career_integration.py tests/career/test_career_setup.py
cd processes/company-discovery/author/core
GOWORK=off go test ./...
```

Historical four-lane implementation modules remain as unregistered source references during the product rebuild. They do not execute through the canonical CLI. The shared `harness_fleet` library retains its own separate contracts and tests.
