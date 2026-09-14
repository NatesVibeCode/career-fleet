"""Declarative task contract loading and preset templates."""
import math
from pathlib import Path
from typing import Any

from .models import TaskSpec


def _checklist_property(items: dict[str, int], descriptions: dict[str, str]) -> dict[str, Any]:
    """Closed boolean-map schema for evidence-bound checklist answers."""
    return {
        "type": "object",
        "description": (
            "Evidence-bound checklist: answer every item true or false; each true "
            "item must be supported by a cited quote. The pipeline computes the score."
        ),
        "properties": {
            item_id: {"type": "boolean", "description": descriptions.get(item_id, f"True when the source supports '{item_id}'.")}
            for item_id in items
        },
        "required": sorted(items),
        "additionalProperties": False,
    }


def _computed_score_property(description: str) -> dict[str, Any]:
    return {
        "type": "integer",
        "minimum": 0,
        "maximum": 100,
        "description": f"Computed by the pipeline from the checklist; omit it. {description}",
    }


SCORE_CHECKLIST = {
    "initiative_named": 40,
    "criteria_evidence": 35,
    "supporting_signals": 25,
}
SCORE_CHECKLIST_DESCRIPTIONS = {
    "initiative_named": "True only when the source names an active explicit initiative, project, or evaluation.",
    "criteria_evidence": "True only when the source confirms stated qualification criteria with cited facts.",
    "supporting_signals": "True only when the source shows supporting signals such as hiring, funding, or leadership change.",
}
SCORE_EVIDENCE_TERMS = [
    "initiative", "migration", "evaluation", "pilot", "hiring", "headcount",
    "funding", "roadmap", "priority", "moderniz", "platform", "scale",
]
ACCOUNT_CHECKLIST = {
    "explicit_initiative": 40,
    "stack_confirmed": 30,
    "hiring_or_trigger": 20,
    "firmographic_fit": 10,
}
ACCOUNT_CHECKLIST_DESCRIPTIONS = {
    "explicit_initiative": "True only when the source names an active explicit initiative or bottleneck, quoted verbatim.",
    "stack_confirmed": "True only when the source confirms required-stack technology in use.",
    "hiring_or_trigger": "True only when the source shows senior infra hiring or a trigger/pain phrase.",
    "firmographic_fit": "True only when the source confirms firmographic fit such as size, sector, or model.",
}
ACCOUNT_EVIDENCE_TERMS = [
    "initiative", "migration", "moderniz", "hiring", "headcount", "platform",
    "bottleneck", "scale", "latency", "outage", "roadmap", "kubernetes",
]
# Initial recency calibration in days: hiring urgency goes stale in weeks
# (a live posting can close any day) while stack and firmographic facts
# last months. Refit against labeled outcomes; these are starting points.
ACCOUNT_HALF_LIVES = {
    "explicit_initiative": 90.0,
    "stack_confirmed": 180.0,
    "hiring_or_trigger": 21.0,
    "firmographic_fit": 365.0,
}

PRESETS: dict[str, dict[str, Any]] = {
    "summarize": {
        "instructions": "Summarize each item using only supported source facts.",
        "properties": {
            "summary": {
                "type": "string",
                "description": "One or two sentences supported only by the cited source quotes, no outside knowledge.",
            },
        },
        "required": ["summary"],
    },
    "classify": {
        "instructions": "Classify each item and give a short supported summary.",
        "properties": {
            "label": {
                "type": "string",
                "description": "The single best-fitting category label for the item, supported by the cited quotes.",
            },
            "summary": {
                "type": "string",
                "description": "One or two sentences supported only by the cited source quotes, no outside knowledge.",
            },
        },
        "required": ["label", "summary"],
    },
    "extract": {
        "instructions": "Extract a short supported summary and the named entities present in the source.",
        "properties": {
            "summary": {
                "type": "string",
                "description": "One or two sentences supported only by the cited source quotes, no outside knowledge.",
            },
            "entities": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Named entities stated verbatim in the source (products, companies, systems), never inferred.",
            },
        },
        "required": ["summary", "entities"],
    },
    "triage": {
        "instructions": "Assign a supported triage priority and explain why.",
        "properties": {
            "priority": {
                "enum": ["high", "medium", "low", "unknown"],
                "description": "Urgency grounded in the source: high only for explicit blocking impact, unknown when the source is silent.",
            },
            "reason": {
                "type": "string",
                "description": "Short explanation naming the source evidence behind the priority.",
            },
        },
        "required": ["priority", "reason"],
    },
    "score": {
        "instructions": "Evaluate each item against qualification criteria by answering the evidence checklist; the pipeline computes the 0-100 score.",
        "checklist": SCORE_CHECKLIST,
        "evidence_terms": SCORE_EVIDENCE_TERMS,
        "properties": {
            "checklist": _checklist_property(SCORE_CHECKLIST, SCORE_CHECKLIST_DESCRIPTIONS),
            "score": _computed_score_property(
                "Qualification score: 85-100 explicit named initiative, 70-84 confirmed criteria plus supporting signals, 50-69 partial fit only, 0-49 incompatible or silent."
            ),
            "reason": {
                "type": "string",
                "description": "Short explanation naming the source evidence behind the checklist answers.",
            },
        },
        "required": ["checklist", "reason"],
    },
    "filter": {
        "instructions": "Screen each item against qualifying criteria, providing a boolean passed status and supported explanation.",
        "properties": {
            "passed": {
                "type": "boolean",
                "description": "True only when the source meets every qualifying criterion with cited evidence; false otherwise.",
            },
            "reason": {
                "type": "string",
                "description": "Short explanation naming the source evidence behind the decision.",
            },
        },
        "required": ["passed", "reason"],
    },
    "account-research": {
        "instructions": "Evaluate target account technical fit by answering the evidence checklist, identify key technical bottlenecks or gaps, and cite verbatim evidence. The pipeline computes the ICP fit score and tier.",
        "checklist": ACCOUNT_CHECKLIST,
        "evidence_terms": ACCOUNT_EVIDENCE_TERMS,
        "recency_half_lives": ACCOUNT_HALF_LIVES,
        "properties": {
            "checklist": _checklist_property(ACCOUNT_CHECKLIST, ACCOUNT_CHECKLIST_DESCRIPTIONS),
            "score": _computed_score_property(
                "ICP fit: 85-100 active explicit initiative quoted verbatim, 70-84 confirmed stack plus senior infra hiring, 50-69 firmographic fit only, 0-49 incompatible stack or wrong model."
            ),
            "identified_gap": {
                "type": "string",
                "description": "The verified technical initiative or bottleneck, named exactly as the source names it.",
            },
            "fit_tier": {
                "enum": ["tier_1", "tier_2", "tier_3", "unfit"],
                "description": "Score band, derived by the pipeline: tier_1 85-100, tier_2 70-84, tier_3 50-69, unfit below 50. Omit it.",
            },
            "reasoning": {
                "type": "string",
                "description": "Short explanation grounded in the cited quotes, naming the evidence behind the checklist and gap.",
            },
        },
        "required": ["checklist", "identified_gap", "reasoning"],
    },
}


def parse_source_weight(spec: str) -> tuple[str, float]:
    """Parse a 'substring=weight' source-weight rule (weight in 0-1)."""
    if "=" not in spec:
        raise ValueError(f"source weight must look like 'example.com=0.5', got {spec!r}")
    match, _, raw = spec.partition("=")
    match = match.strip().casefold()
    if not match:
        raise ValueError(f"source weight needs a non-empty match, got {spec!r}")
    try:
        weight = float(raw)
    except ValueError as exc:
        raise ValueError(f"source weight for '{match}' is not numeric: {raw!r}") from exc
    if not 0.0 <= weight <= 1.0:
        raise ValueError(f"source weight for '{match}' must be between 0 and 1")
    return match, weight


def parse_half_life(spec: str) -> tuple[str, float]:
    """Parse an 'item=days' recency half-life rule (days must be positive)."""
    if "=" not in spec:
        raise ValueError(f"half-life must look like 'hiring_or_trigger=21', got {spec!r}")
    item_id, _, raw = spec.partition("=")
    item_id = item_id.strip()
    if not item_id:
        raise ValueError(f"half-life needs a non-empty item id, got {spec!r}")
    try:
        days = float(raw)
    except ValueError as exc:
        raise ValueError(f"half-life for '{item_id}' is not numeric: {raw!r}") from exc
    if not math.isfinite(days) or days <= 0:
        raise ValueError(f"half-life for '{item_id}' must be a positive number of days")
    return item_id, days


def create_task_from_preset(
    name: str,
    preset_name: str = "score",
    instructions: str | None = None,
    batch_size: int = 5,
    source_weights: dict[str, float] | None = None,
    default_source_weight: float = 1.0,
    recency_half_lives: dict[str, float] | None = None,
) -> TaskSpec:
    if preset_name not in PRESETS:
        raise ValueError(f"unknown preset '{preset_name}'; available presets: {', '.join(sorted(PRESETS))}")
    preset = PRESETS[preset_name]
    return TaskSpec(
        name=name,
        instructions=instructions or preset["instructions"],
        batch_size=batch_size,
        claims_schema={
            "type": "object",
            "properties": preset["properties"],
            "required": preset["required"],
            "additionalProperties": False,
        },
        checklist=dict(preset["checklist"]) if preset.get("checklist") else None,
        pass_score=preset.get("pass_score"),
        evidence_terms=list(preset.get("evidence_terms") or []),
        source_weights=dict(source_weights or {}),
        default_source_weight=default_source_weight,
        recency_half_lives={
            **(preset.get("recency_half_lives") or {}),
            **(recency_half_lives or {}),
        },
    )


def load_task_spec(task_path: str | Path) -> TaskSpec:
    path = Path(task_path)
    if path.suffix.lower() != ".json":
        raise ValueError("task specs must be JSON files")
    if not path.is_file():
        raise FileNotFoundError(f"task file not found: {path}")
    return TaskSpec.model_validate_json(path.read_text(encoding="utf-8"))
