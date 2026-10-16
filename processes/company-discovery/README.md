# Career Fleet discovery Process

This directory owns the editable source and exact Forge authoring material for Career Fleet's discovery operation. It belongs to `NatesVibeCode/career-fleet`.

- `author/core/`: deterministic task, vocabulary, evidence, matching, question planning, condition, feedback, comparison and reuse functions, with representative local behavior checks.
- `nodes/`: exact source closures and entry adapters selected by this authored revision.
- `specs/`: exact typed Node definitions.
- `plan.json`: Node selections, pinned Block composition, Process-owned progression and observation recovery.
- `stores.json`: minimal selected build-time declaration material. These records confer no admission or service authority.
- `html-LICENSE`: selected upstream tokenizer attribution.

The sovereign runtime snapshot ships under `career_fleet/resources/company_discovery`. It is compiled by the product's `build` command; execution uses only that worker. The Python product adapter does not schedule or select business actions.

## Author through Forge

With the installed standard Forge front door:

```sh
forge-author plan -check --input processes/company-discovery/plan.json \
  --stores processes/company-discovery/stores.json
forge-author plan --input processes/company-discovery/plan.json \
  --stores processes/company-discovery/stores.json \
  --out build/company-discovery-next --develop
python3 scripts/package_discovery_process.py --package build/company-discovery-next/package
```

Choose a new output directory for every authored revision. Node changes require exact source/test bindings and a new print. Do not edit the generated runtime snapshot and keep its old identity. Core source changes must be lowered into the selected Node closures and reauthored through Forge; they do not silently alter an existing print.

The captured plan uses a once-only initialization Block followed by native Process phases. The structural coverage validator currently omits that existing initialization-to-first-phase dependency. Fixing that owner-level gap is still required for a complete requirement manifest and native verification suite.

The current runtime remains an unsigned development print. Function behavior tests, public source observations and successful printing do not confer authenticated Node admission, release qualification or native AO consumption. Optional semantic assessment is not configured. No tests, profiles, credentials or provider captures from the old Documents workspace are part of runtime authority.

## Reconciliation evidence

The previous product's source-specific discovery, automatic employer-profile loading, fixed screening lanes and qualified-only export did not satisfy the selected generic discovery contract. The canonical CLI now runs this Process and appends exact outputs to new SQLite tables. Existing career data is retained. An empty/failed later run does not remove old leads; identity decisions and source/condition judgments remain in the Process.

Local repository discovery identified the existing GitHub repository and command surface directly. The installed navigation dictionary had no entry for this newly restored checkout; no dictionary ownership claim or sealed discovery receipt is asserted.
