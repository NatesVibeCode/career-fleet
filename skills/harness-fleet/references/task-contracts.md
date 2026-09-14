# Task contracts

Read this when creating or importing a task.

Prefer a preset:

```bash
harness-fleet init score-demo --preset score
harness-fleet init filter-demo --preset filter
harness-fleet init labels --preset classify
harness-fleet init facts --preset extract
harness-fleet init queue --preset triage
harness-fleet init summaries --preset summarize
```

SQLite stores immutable task revisions and selects one current revision per task name. A supplied TaskSpec JSON is validated and registered before use.

## InputItem

```json
{
  "item_id": "item_1",
  "text": "Required source text",
  "title": "Optional",
  "source_uri": "https://example.com/source",
  "content_type": "text/plain",
  "metadata": {}
}
```

Only `item_id` and `text` are required. The object is closed. Do not substitute `id`, `body`, `content`, or other aliases. Invalid JSONL lines and duplicate IDs are errors.

## TaskSpec

```json
{
  "$schema": "https://raw.githubusercontent.com/NatesVibeCode/harness-fleet/master/schemas/task-v1.schema.json",
  "format_version": "harness_fleet_task_v1",
  "name": "product-triage",
  "instructions": "Extract a supported summary and category.",
  "batch_size": 4,
  "max_slice_chars": 6000,
  "min_quote_chars": 15,
  "claims_schema": {
    "type": "object",
    "properties": {
      "summary": {"type": "string"},
      "category": {"enum": ["a", "b", "unknown"]}
    },
    "required": ["summary", "category"],
    "additionalProperties": false
  }
}
```

Put extraction meaning in `instructions`. Put field names, types, enums, required status, and closure in `claims_schema`. A task may also carry a `checklist` (item id to score points), a `pass_score` threshold, and `evidence_terms` for candidate ranking; the revision digest ignores defaulted calibration so pre-upgrade stored revisions keep validating.

## Model boundary

The model returns `item_id`, `claims`, and `quotes`. Each quote requires `slice_id` and exact `text`; `start` and `end` are optional. Scoring presets do not collect a judged `score`: the worker answers an evidence-bound `checklist` of true/false items, and the pipeline derives `score` (summed points, capped at 100), `fit_tier`, and `passed`. Supplied values for derived fields must match the derivation; omit them.

Each quote carries `supports` naming the checklist items it backs. Every true answer needs at least one supporting quote (coverage is verified; untagged truth scores zero). A task may price sources differently via `source_weights` (URI substring to 0-1 weight, longest match wins, 0 blocks support) and `default_source_weight`: each true answer contributes its points scaled by its strongest backing source. Set weights at task creation (`harness-fleet init NAME --preset score --source-weight boards.greenhouse.io=1 --source-weight aggregator.example=0.4`).

New evidence arrives as new items, not merged text: one source per item keeps every score traceable to its evidence. Group rounds of the same subject with `metadata.entity` (else the `item_id` is the entity). `harness-fleet rescore PARENT_RUN --input new.jsonl [--task RECALIBRATED]` scores fresh evidence as a new run linked by `parent_run_id`; every verified record lands in `score_history`, and `harness-fleet history ENTITY` (or MCP `harness_fleet_history`) shows the trajectory across rounds.

Checklist points, source weights, and half-lives are fitted, not guessed: `harness-fleet calibrate TASK --input labeled.jsonl --expected score --route JUDGE [--apply]` runs one rater over labeled samples and refines calibration with deterministic coordinate descent (dry run unless `--apply`). Points stay on the simplex so tier bands hold; absolute rater level stays in per-route bias correction.

If the text occurs exactly once in that slice, harness-fleet computes its absolute offsets. If it occurs more than once, the response must include offsets. Sections may also list numbered evidence-candidate spans ranked by term overlap: cite `candidate_id` and copy the span text exactly, and offsets can be omitted because verification recomputes the same span table from the section text. A cited id whose text does not match its span fails closed. The stored `ModelOutput` always includes offsets, source URI, content type, and a SHA-256 source digest. The exported packet embeds this exact TaskSpec, binds it to its revision digest, and revalidates every record against `claims_schema` when written or read.

Every prompt starts with a fixed form-fill template (`TaskSpec.render_instructions`) plus the task direction, and carries a deterministic FIELD RULES block generated from the TaskSpec (`TaskSpec.render_worker_guide`): the real `min_quote_chars`, the once-only offsets rule, the checklist points and which fields are computed, the candidate-citation rule when terms are configured, the score/tier bands when the schema has both fields, and per-field descriptions. Workers fill typed fields the way the verifier checks them; nothing with a number in it is left to model discretion.

Use `harness-fleet schema KIND` to inspect the admitted `task`, `input`, `candidate-output`, `output`, `packet`, or `database` contract.
