"""Declarative task contract loading and preset templates."""
from pathlib import Path
from typing import Any

from .models import TaskSpec

PRESETS: dict[str, dict[str, Any]] = {
    "summarize": {
        "instructions": "Summarize each item using only supported source facts.",
        "properties": {"summary": {"type": "string"}},
        "required": ["summary"],
    },
    "classify": {
        "instructions": "Classify each item and give a short supported summary.",
        "properties": {"label": {"type": "string"}, "summary": {"type": "string"}},
        "required": ["label", "summary"],
    },
    "extract": {
        "instructions": "Extract a short supported summary and the named entities present in the source.",
        "properties": {
            "summary": {"type": "string"},
            "entities": {"type": "array", "items": {"type": "string"}},
        },
        "required": ["summary", "entities"],
    },
    "triage": {
        "instructions": "Assign a supported triage priority and explain why.",
        "properties": {
            "priority": {"enum": ["high", "medium", "low", "unknown"]},
            "reason": {"type": "string"},
        },
        "required": ["priority", "reason"],
    },
    "score": {
        "instructions": "Evaluate each item against qualification criteria, assigning a numerical score from 0 to 100 and concise reasoning grounded in source evidence.",
        "properties": {
            "score": {"type": "integer", "minimum": 0, "maximum": 100},
            "reason": {"type": "string"},
        },
        "required": ["score", "reason"],
    },
    "filter": {
        "instructions": "Screen each item against qualifying criteria, providing a boolean passed status and supported explanation.",
        "properties": {
            "passed": {"type": "boolean"},
            "reason": {"type": "string"},
        },
        "required": ["passed", "reason"],
    },
    "account-research": {
        "instructions": "Evaluate target account technical fit, assign an ICP fit score (0-100), identify key technical bottlenecks or gaps, and cite verbatim evidence.",
        "properties": {
            "score": {"type": "integer", "minimum": 0, "maximum": 100},
            "identified_gap": {"type": "string"},
            "fit_tier": {"enum": ["tier_1", "tier_2", "tier_3", "unfit"]},
        },
        "required": ["score", "identified_gap", "fit_tier"],
    },
}


def create_task_from_preset(
    name: str,
    preset_name: str = "score",
    instructions: str | None = None,
    batch_size: int = 5,
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
    )


def load_task_spec(task_path: str | Path) -> TaskSpec:
    path = Path(task_path)
    if path.suffix.lower() != ".json":
        raise ValueError("task specs must be JSON files")
    if not path.is_file():
        raise FileNotFoundError(f"task file not found: {path}")
    return TaskSpec.model_validate_json(path.read_text(encoding="utf-8"))

