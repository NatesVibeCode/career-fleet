# Career Fleet research reference

Use [workflow.md](workflow.md) for the current command and request contract.

The rebuilt CLI uses the bundled printed Process. Supply criteria, vocabulary and research questions explicitly. There is no inferred employer profile, default fit formula, automatic model assessment or four-lane fallback.


The canonical MCP front door is `career-fleet serve --workspace-root /absolute/workspace --db research.db` over stdio. Its init, source-discovery, source-verification and campaign tools call the same printed Process as the CLI. Native Block artifacts ship under `career_fleet/resources/company_discovery/source-blocks/`; current real-use observations are indexed in `career_fleet/resources/source_block_evidence.json`. Use `blocks list` and `blocks verify`, or their MCP tools. The older Harness Fleet server is separate.
