"""Declarative task contract loading and preset templates."""
import math
from copy import deepcopy
from pathlib import Path
from typing import Any

from .models import TIER_BY_SCORE, TaskSpec


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
# Career screening checklist, derived from the 6 core career signals and the
# 4-tier rubric in the career-fleet skill. Points must total 100: the pipeline
# turns the boolean answers into the fit score, so the model never grades.
CAREER_CHECKLIST = {
    "wedge_alignment": 25,
    "stack_confirmed": 25,
    "hiring_catalyst": 25,
    "leadership_signal": 15,
    "workplace_match": 10,
}
CAREER_CHECKLIST_DESCRIPTIONS = {
    "wedge_alignment": "True only when the source names a concrete problem the candidate's wedge solves, not a generic role summary.",
    "stack_confirmed": "True only when the source evidences the required technologies running in production.",
    "hiring_catalyst": "True only when the source names the operational failure, milestone, or transition creating the mandate.",
    "leadership_signal": "True only when the source shows the target leader persona: low-ego technical builders.",
    "workplace_match": "True only when the source states a workplace arrangement matching the profile's selected policy.",
}
CAREER_EVIDENCE_TERMS = [
    "hiring", "founding", "scaling", "migration", "architecture", "platform",
    "latency", "reliability", "infrastructure", "roadmap", "ownership", "series",
]
# Initial recency calibration in days: a live posting can close any day, so the
# catalyst goes stale fastest; leadership and stack facts last months. Refit
# against labeled outcomes; these are starting points.
CAREER_HALF_LIVES = {
    "wedge_alignment": 180.0,
    "stack_confirmed": 180.0,
    "hiring_catalyst": 21.0,
    "leadership_signal": 365.0,
    "workplace_match": 90.0,
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
    "career-screening": {
        "instructions": "Screen this employer and role against the candidate's stated criteria by answering the evidence checklist, name the specific problem the role exists to solve, and cite verbatim evidence. The pipeline computes the fit score and tier.",
        "checklist": CAREER_CHECKLIST,
        "evidence_terms": CAREER_EVIDENCE_TERMS,
        "recency_half_lives": CAREER_HALF_LIVES,
        "properties": {
            "checklist": _checklist_property(CAREER_CHECKLIST, CAREER_CHECKLIST_DESCRIPTIONS),
            "score": _computed_score_property(
                "Fit: 85-100 every signal evidenced with a named catalyst, 70-84 required stack plus strong wedge alignment, 50-69 acceptable stack but unclear defensibility or one missing requirement, 0-49 a hard dealbreaker or a missing required-stack entry."
            ),
            "identified_wedge": {
                "type": "string",
                "description": "The specific problem this role exists to solve, named exactly as the source names it.",
            },
            "fit_tier": {
                "enum": ["tier_1", "tier_2", "tier_3", "unfit"],
                "description": "Score band, derived by the pipeline: tier_1 85-100, tier_2 70-84, tier_3 50-69, unfit below 50. Omit it.",
            },
            "reasoning": {
                "type": "string",
                "description": "Short explanation grounded in the cited quotes, naming the evidence behind the checklist and the wedge.",
            },
        },
        "required": ["checklist", "identified_wedge", "reasoning"],
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


# Fields a scoring editor may change. Everything else in a TaskSpec —
# especially score, fit_tier, and passed — is derived by the pipeline from
# checklist answers, so exposing them would reintroduce free-form grading.
SCORING_EDITABLE_FIELDS = frozenset({
    "instructions",
    "checklist",
    "source_weights",
    "default_source_weight",
    "pass_score",
    "evidence_terms",
    "half_lives",
    "min_quote_chars",
    "batch_size",
    "max_slice_chars",
    "candidate_top_n",
})
SCORING_LOCKED_FIELDS = frozenset({"score", "fit_tier", "passed", "tier_bands", "claims_schema"})
_CHECKLIST_ITEM_FIELDS = frozenset({"item_id", "points", "description", "half_life_days"})


def tier_bands() -> list[dict[str, Any]]:
    """Read-only 0-100 score bands, in descending order of minimum score."""
    ordered = sorted(TIER_BY_SCORE, key=lambda pair: pair[0], reverse=True)
    bands: list[dict[str, Any]] = []
    for index, (threshold, tier) in enumerate(ordered):
        upper = 100 if index == 0 else ordered[index - 1][0] - 1
        bands.append({"tier": tier, "min_score": threshold, "max_score": upper})
    return bands


def _checklist_descriptions(spec: TaskSpec) -> dict[str, str]:
    """Item descriptions carried in the claims schema's checklist property."""
    properties = spec.claims_schema.get("properties", {}) if isinstance(spec.claims_schema, dict) else {}
    checklist_prop = properties.get("checklist") if isinstance(properties, dict) else None
    item_props = checklist_prop.get("properties", {}) if isinstance(checklist_prop, dict) else {}
    descriptions: dict[str, str] = {}
    for item_id, described in (item_props or {}).items():
        if isinstance(described, dict):
            descriptions[str(item_id)] = str(described.get("description") or "")
    return descriptions


def scoring_view(spec: TaskSpec) -> dict[str, Any]:
    """Plain-language read model of a task's scoring contract.

    This is a view over the typed TaskSpec, not a second scoring engine:
    checklist items and points are editable calibration, while score,
    fit_tier, and passed stay pipeline-derived and are reported as fixed.
    """
    properties = spec.claims_schema.get("properties", {}) if isinstance(spec.claims_schema, dict) else {}
    descriptions = _checklist_descriptions(spec)
    items = [
        {
            "item_id": item_id,
            "points": int(points),
            "description": descriptions.get(item_id, ""),
            "half_life_days": spec.recency_half_lives.get(item_id),
        }
        for item_id, points in (spec.checklist or {}).items()
    ]
    return {
        "task": spec.name,
        "scorable": spec.checklist is not None,
        "derived": True,
        "instructions": spec.instructions,
        "checklist": items,
        "total_points": sum(int(points) for points in (spec.checklist or {}).values()),
        "score_cap": 100,
        "tier_bands": tier_bands(),
        "has_fit_tier": isinstance(properties, dict) and "fit_tier" in properties,
        "has_pass": isinstance(properties, dict) and "passed" in properties,
        "pass_score": spec.pass_score,
        "source_weights": dict(spec.source_weights),
        "default_source_weight": float(spec.default_source_weight),
        "evidence_terms": list(spec.evidence_terms),
        "min_quote_chars": spec.min_quote_chars,
        "batch_size": spec.batch_size,
        "max_slice_chars": spec.max_slice_chars,
        "candidate_top_n": spec.candidate_top_n,
    }


def _positive_days(item_id: str, value: Any) -> float:
    if isinstance(value, bool):
        raise ValueError(f"half-life for '{item_id}' must be a number of days")
    try:
        days = float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"half-life for '{item_id}' must be a number of days") from exc
    if not math.isfinite(days) or days <= 0:
        raise ValueError(f"half-life for '{item_id}' must be a positive number of days")
    return days


def _integer_field(name: str, value: Any, low: int, high: int) -> int:
    """Validate an integer field, rejecting bools, floats, and out-of-range values."""
    if isinstance(value, bool):
        raise ValueError(f"{name} must be an integer")
    try:
        number = float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"{name} must be an integer") from exc
    if not math.isfinite(number) or not number.is_integer():
        raise ValueError(f"{name} must be an integer")
    result = int(number)
    if not low <= result <= high:
        raise ValueError(f"{name} must be between {low} and {high}")
    return result


def _edited_checklist(
    patch: Any,
    descriptions: dict[str, str],
    existing_half_lives: dict[str, float],
) -> tuple[dict[str, int], dict[str, str], dict[str, float]]:
    """Validate an edited checklist into items, descriptions, and half-lives.

    A checklist entry is a full replacement for that item. An omitted
    ``half_life_days`` key preserves the stored value; an explicit null or
    empty string clears it, so a client that posts partial rows cannot
    silently drop recency calibration.
    """
    if not isinstance(patch, list) or not patch:
        raise ValueError("checklist must be a non-empty list of items")
    items: dict[str, int] = {}
    edited_descriptions: dict[str, str] = {}
    half_lives: dict[str, float] = {}
    for entry in patch:
        if not isinstance(entry, dict):
            raise ValueError("each checklist item must be an object")
        unknown = sorted(set(entry) - _CHECKLIST_ITEM_FIELDS)
        if unknown:
            raise ValueError(f"unsupported checklist item fields: {unknown}")
        item_id = str(entry.get("item_id") or "").strip()
        if not item_id:
            raise ValueError("checklist item ids must be non-empty strings")
        if item_id in items:
            raise ValueError(f"duplicate checklist item id: {item_id}")
        points = _integer_field(f"checklist points for '{item_id}'", entry.get("points"), 1, 100)
        items[item_id] = points
        # Blank descriptions fall through to _checklist_property's generic text.
        text = str(entry.get("description") or descriptions.get(item_id, "")).strip()
        if text:
            edited_descriptions[item_id] = text
        if "half_life_days" in entry:
            half_life = entry.get("half_life_days")
            if half_life not in (None, ""):
                half_lives[item_id] = _positive_days(item_id, half_life)
        elif item_id in existing_half_lives:
            half_lives[item_id] = float(existing_half_lives[item_id])
    return items, edited_descriptions, half_lives


def apply_scoring_edit(spec: TaskSpec, patch: Any) -> TaskSpec:
    """Return a new TaskSpec with structured scoring edits applied.

    Only evidence-level calibration is accepted: checklist items and points,
    source weights, recency half-lives, the pass threshold, and the
    instruction text. Score, fit_tier, and passed are pipeline-derived, and
    a replacement claims_schema is rejected, so a UI can tune the rubric
    without ever grading a record by hand.
    """
    if not isinstance(patch, dict):
        raise ValueError("scoring patch must be a JSON object")
    unknown = sorted(set(patch) - SCORING_EDITABLE_FIELDS)
    if unknown:
        locked = sorted(set(unknown) & SCORING_LOCKED_FIELDS)
        if locked:
            raise ValueError(
                f"{', '.join(locked)} is derived by the pipeline and cannot be edited; "
                "edit the evidence checklist and weights instead"
            )
        raise ValueError(f"unsupported scoring fields: {unknown}")
    if spec.checklist is None:
        raise ValueError(f"task '{spec.name}' has no scoring checklist to edit")

    payload = spec.model_dump(mode="json", by_alias=True)
    half_lives = dict(spec.recency_half_lives)
    effective_items = dict(spec.checklist)

    if "instructions" in patch:
        text = str(patch["instructions"]).strip()
        if not text:
            raise ValueError("instructions must not be empty")
        payload["instructions"] = text

    if "checklist" in patch:
        items, descriptions, half_lives = _edited_checklist(
            patch["checklist"], _checklist_descriptions(spec), spec.recency_half_lives
        )
        effective_items = items
        payload["checklist"] = items
        schema = deepcopy(spec.claims_schema)
        schema.setdefault("properties", {})["checklist"] = _checklist_property(items, descriptions)
        payload["claims_schema"] = schema

    if "half_lives" in patch:
        raw_halves = patch["half_lives"]
        if not isinstance(raw_halves, dict):
            raise ValueError("half_lives must be an object of checklist item id to days")
        half_lives = {}
        for item_id, value in raw_halves.items():
            if item_id not in effective_items:
                raise ValueError(f"half-life for '{item_id}' names no checklist item")
            half_lives[item_id] = _positive_days(item_id, value)
    payload["recency_half_lives"] = half_lives

    if "source_weights" in patch:
        raw_weights = patch["source_weights"]
        if not isinstance(raw_weights, dict):
            raise ValueError("source_weights must be an object of URI substring to weight")
        weights: dict[str, float] = {}
        for match, value in raw_weights.items():
            key = str(match).strip().casefold()
            if not key:
                raise ValueError("source weight keys must be non-empty strings")
            if isinstance(value, bool):
                raise ValueError(f"source weight for '{key}' must be a number 0-1")
            try:
                weight = float(value)
            except (TypeError, ValueError) as exc:
                raise ValueError(f"source weight for '{key}' must be a number 0-1") from exc
            if not math.isfinite(weight) or not 0.0 <= weight <= 1.0:
                raise ValueError(f"source weight for '{key}' must be between 0 and 1")
            weights[key] = weight
        payload["source_weights"] = weights

    if "default_source_weight" in patch:
        if isinstance(patch["default_source_weight"], bool):
            raise ValueError("default_source_weight must be a number 0-1")
        try:
            default_weight = float(patch["default_source_weight"])
        except (TypeError, ValueError) as exc:
            raise ValueError("default_source_weight must be a number 0-1") from exc
        if not math.isfinite(default_weight) or not 0.0 <= default_weight <= 1.0:
            raise ValueError("default_source_weight must be between 0 and 1")
        payload["default_source_weight"] = default_weight

    if "pass_score" in patch:
        value = patch["pass_score"]
        if value in (None, ""):
            payload["pass_score"] = None
        else:
            payload["pass_score"] = _integer_field("pass_score", value, 0, 100)

    if "evidence_terms" in patch:
        raw_terms = patch["evidence_terms"]
        if not isinstance(raw_terms, list):
            raise ValueError("evidence_terms must be a list of non-empty strings")
        terms = []
        for term in raw_terms:
            text = str(term).strip()
            if not text:
                raise ValueError("evidence_terms must be non-empty strings")
            terms.append(text)
        payload["evidence_terms"] = terms

    # Bounds mirror the TaskSpec field constraints, so invalid values fail here
    # with a readable message instead of a pydantic ValidationError dump.
    for field, low, high in (
        ("min_quote_chars", 1, 10_000),
        ("batch_size", 1, 100),
        ("max_slice_chars", 300, 100_000),
        ("candidate_top_n", 1, 32),
    ):
        if field in patch:
            payload[field] = _integer_field(field, patch[field], low, high)

    return TaskSpec.model_validate(payload)


def duplicate_spec(spec: TaskSpec, new_name: str) -> TaskSpec:
    """Copy a task contract under a new name.

    Round-trips through model_validate so the copy is re-checked, and clears
    nothing else: the duplicate starts as an exact, separately-revisioned
    clone that can be tuned without touching the original.
    """
    payload = spec.model_dump(mode="json", by_alias=True)
    payload["name"] = new_name
    return TaskSpec.model_validate(payload)


def load_task_spec(task_path: str | Path) -> TaskSpec:
    path = Path(task_path)
    if path.suffix.lower() != ".json":
        raise ValueError("task specs must be JSON files")
    if not path.is_file():
        raise FileNotFoundError(f"task file not found: {path}")
    return TaskSpec.model_validate_json(path.read_text(encoding="utf-8"))
