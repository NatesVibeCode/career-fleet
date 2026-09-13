"""Typed Ideal Company Profile used by account-fleet onboarding."""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel, ConfigDict, Field, JsonValue, model_validator


class IdealCompanyProfile(BaseModel):
    """The durable ICP contract for account research.

    The JSON file is the human-editable authoring form. The SQLite store keeps
    immutable revisions so a run can be traced back to the exact ICP that was
    selected when it started.
    """

    model_config = ConfigDict(extra="forbid")

    profile_kind: ClassVar[str] = "ideal_company"

    profile_name: str = Field(default="My Ideal Company Profile")
    version: str = Field(default="1.0.0")
    product_category: str = Field(default="")
    architectural_layer: str = Field(default="")
    required_stack: list[str] = Field(default_factory=list)
    negative_stack_exclusions: list[str] = Field(default_factory=list)
    trigger_pain_phrases: list[str] = Field(default_factory=list)
    target_roles: list[str] = Field(default_factory=list)
    anchor_logos: list[str] = Field(default_factory=list)
    calibrated_scoring_rubric: dict[str, JsonValue] = Field(default_factory=dict)

    @model_validator(mode="before")
    @classmethod
    def accept_interview_document(cls, value: Any) -> Any:
        """Flatten the nested shape emitted by the ICP interview guide."""
        if not isinstance(value, dict) or not isinstance(value.get("icp_profile"), dict):
            return value
        flattened = dict(value["icp_profile"])
        for key in ("profile_name", "version", "calibrated_scoring_rubric"):
            if key in value:
                flattened[key] = value[key]
        return flattened

    @classmethod
    def load(cls, path: Path | str) -> "IdealCompanyProfile":
        profile_path = Path(path).expanduser()
        if not profile_path.exists():
            raise FileNotFoundError(f"Ideal Company Profile not found at: {profile_path}")
        return cls.model_validate(json.loads(profile_path.read_text(encoding="utf-8")))

    def save(self, path: Path | str) -> None:
        profile_path = Path(path).expanduser()
        profile_path.parent.mkdir(parents=True, exist_ok=True)
        profile_path.write_text(self.model_dump_json(indent=2), encoding="utf-8")

    def to_prompt_context(self) -> str:
        """Render explicit ICP context for an account task when requested."""
        return (
            f"IDEAL COMPANY PROFILE: {self.profile_name} (v{self.version})\n"
            f"Product category: {self.product_category or 'Not specified'}\n"
            f"Architectural layer: {self.architectural_layer or 'Not specified'}\n"
            f"Required stack: {', '.join(self.required_stack) or 'Not specified'}\n"
            f"Negative stack exclusions: {', '.join(self.negative_stack_exclusions) or 'None specified'}\n"
            f"Trigger/pain phrases: {', '.join(self.trigger_pain_phrases) or 'Not specified'}\n"
            f"Target roles: {', '.join(self.target_roles) or 'Not specified'}\n"
            f"Anchor logos: {', '.join(self.anchor_logos) or 'None specified'}\n"
            f"Scoring rubric: {json.dumps(self.calibrated_scoring_rubric, ensure_ascii=False, sort_keys=True)}"
        )
