---
name: career-fleet
description: Run Career Fleet discovery from an explicit research request, retain unresolved leads, inspect source-bound results, and export exact executions.
---

# Career Fleet research

Use the canonical `career-fleet` CLI or its stdio MCP tools. Read `references/workflow.md` before execution and `references/onboarding.md` for installation.

The discovery Process accepts a generic objective or supplied collection. Criteria, vocabulary, sources, research questions, attribution and acquisition allowances are explicit request data. Do not load ambient personal files, invent an employer profile, or impose a default checklist.

Any data for any listed business may be requested at any time. Carry selected businesses as `prior` and supply the requested `questions` and extraction rules. Fields have no assigned rung or required enrichment order; unresolved identity and already populated fields do not prevent a request. Preserve evidence age and provenance when reusing data, and use explicit refresh when fresh acquisition is requested.

`career-fleet build` compiles the bundled standalone worker. `career-fleet init --objective "..." --output request.json` creates a generic request with the broad public source catalog and acquisition disabled. The catalog spans web search, news, startup and software directories, marketplaces, developer catalogs, and community search. Use `--pages N` to select pagination and `--enable-acquisition` to authorize public reads in that request; `--max-requests N` is an optional request-level allowance, not a product-wide ceiling. `--empty-sources` explicitly opts out of the catalog. `--enable-browser` selects pinned rendered-DOM acquisition for JavaScript-heavy marketplace channels; those requests need `discover --browser-runtime browser-runtime.json` with the local runtime paths. Then run `career-fleet discover --request request.json --db career_fleet.db`.

Static HTML and browser-rendered search sources use separate Process acquisition phases. Browser reads use a pinned, caller-bound Node/Playwright/Chromium runtime, preserve the rendered DOM and original source-response bytes, and report denied resources as coverage gaps. JSON sources use declared field mappings, and every search action retains its source id, query, URL, capture, and outcome.

The printed Process owns matching, scheduling, capture, parsing, evidence, identity, condition evaluation and recovery. The CLI owns launch and recording only. Never replace the Process with a Python browsing loop or a generic model loop.

Keep all sparse, unresolved, conflicting and deferred leads visible. Missing information is not a negative fit score. Source count, copied publicity and company age do not improve merit. Shared domains do not establish company identity. No generative inference is configured in this delivery.

Use `list`, `dossier --id ID`, and `export`. `export --run EVENT_ID` returns the exact execution. Preserve source failures and coverage; a failed read does not erase a company.

The bundled CLI/MCP package is explicitly a development print. Check exact evidence before claiming Node admission, release printing or native AO consumption; those are separate stages, and a separately printed release does not change this bundle's stage. Old profile, triage, recon and board commands are not fallbacks for this Process.
