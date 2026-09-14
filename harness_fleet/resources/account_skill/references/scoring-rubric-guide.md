# Scoring Rubric & Task Spec Guide

This guide explains how to construct a deterministic 0–100 scoring contract so that LLMs evaluate accounts consistently and extract character-exact evidence.

Keep the reusable ICP separate from the task rubric. Store the confirmed
`IdealCompanyProfile` with `account-fleet profile`; the task revision describes
the output fields and scoring instructions for one campaign. Runs retain the
profile revision ID in SQLite so a result can be reviewed against the exact
profile that was active.

---

## 1. The 4-Tier Scoring Rubric

To prevent model score inflation or arbitrary grading, enforce this standardized 4-tier rubric across all account research tasks:

| Tier | Score Range | Definition | Grounding Requirement |
|---|---|---|---|
| **Tier 1 (Immediate Fit)** | **85 – 100** | Active, explicit initiative cited directly in the source text (e.g. legacy system replacement, scaling limit, active migration). | **Mandatory verbatim quote** naming the exact initiative. |
| **Tier 2 (High Potential)** | **70 – 84** | Target tech stack is confirmed and senior infrastructure hiring is active, but specific bottleneck is implied rather than explicitly named. | Quote proving presence of stack and seniority of role. |
| **Tier 3 (Firmographic Only)** | **50 – 69** | Company fits general industry/size criteria, but no evidence of target architecture or active engineering pain. | General company description quote. |
| **Unfit (Discard)** | **0 – 49** | Uses competing/incompatible stack, wrong business model, or outdated technology. | None (auto-filtered out of deliverable). |

---

## 2. Complete TaskSpec Template

When initializing a task via CLI or MCP (`account-fleet init` or `harness_fleet_init`), use this declarative TaskSpec structure:

```json
{
  "$schema": "https://raw.githubusercontent.com/NatesVibeCode/harness-fleet/master/schemas/task-v1.schema.json",
  "format_version": "harness_fleet_task_v1",
  "name": "target-account-qualification",
  "instructions": "Evaluate each company based on ICP fit. Assign an integer score from 0 to 100 based on the 4-tier rubric. For any score >= 70, identify the technical gap and extract an exact verbatim quote (at least 15 characters) from the source text proving the bottleneck or initiative. If the company uses incompatible technology or has no technical fit, assign a score < 50.",
  "batch_size": 4,
  "max_slice_chars": 6000,
  "min_quote_chars": 15,
  "claims_schema": {
    "type": "object",
    "properties": {
      "score": {
        "type": "integer",
        "minimum": 0,
        "maximum": 100,
        "description": "ICP qualification score (0-100)"
      },
      "identified_gap": {
        "type": "string",
        "description": "Concise summary of the verified technical initiative or bottleneck"
      },
      "fit_tier": {
        "enum": ["tier_1", "tier_2", "tier_3", "unfit"],
        "description": "tier_1 (85-100), tier_2 (70-84), tier_3 (50-69), unfit (<50)"
      },
      "reasoning": {
        "type": "string",
        "description": "Short explanation grounded in the cited source evidence"
      }
    },
    "required": ["score", "identified_gap", "fit_tier", "reasoning"],
    "additionalProperties": false
  }
}
```

---

## 3. How the Verification Gate Operates

When the free model returns candidate results:

```json
{
  "claims": {
    "score": 98,
    "identified_gap": "Legacy billing migration to Kafka",
    "fit_tier": "tier_1",
    "reasoning": "The source explicitly describes a legacy billing migration to Kafka."
  },
  "quotes": [
    {
      "slice_id": "full",
      "text": "leading the migration of our legacy billing service to Apache Kafka"
    }
  ]
}
```

Account-Fleet runs the Python verification gate:
```python
# 1. Exact Substring Match
if quote["text"] not in raw_text:
    raise GroundingFailure("Quote text not found verbatim in source")

# 2. Minimum Length Guard
if len(quote["text"].strip()) < 15:
    raise GroundingFailure("Quote is shorter than minimum threshold")

# 3. Deterministic Offset Resolution
start = raw_text.index(quote["text"])
end = start + len(quote["text"])
```

If the model hallucinates or paraphrases text that doesn't appear character-for-character in `raw_text`, the entire record fails verification and rotates to the next worker route. **Zero hallucinated claims can enter the exported CSV.**

Score/tier consistency is enforced the same deterministic way: when claims carry both `score` and `fit_tier`, the tier must equal the score's band (85+ → `tier_1`, 70+ → `tier_2`, 50+ → `tier_3`, else `unfit`). A mismatched tier fails validation and rotates routes — author the rubric bands, not per-record tier judgment.
