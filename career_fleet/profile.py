"""Ideal Employer Profile (IEP) and candidate fit specifications."""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Dict, List, Literal, Optional
from pydantic import BaseModel, ConfigDict, Field


class Dealbreakers(BaseModel):
    model_config = ConfigDict(extra="ignore")

    max_headcount: Optional[int] = Field(
        default=80,
        description="Maximum company headcount before bureaucracy and silos take over."
    )
    policy: Literal["remote_only", "remote_or_hybrid", "any"] = Field(
        default="remote_only",
        description="Workplace policy requirement."
    )
    disallowed_locations: List[str] = Field(
        default_factory=lambda: ["San Francisco in-office mandate", "New York in-office mandate"],
        description="Mandatory in-office locations that trigger disqualification."
    )
    reject_thin_wrappers: bool = Field(
        default=True,
        description="Reject shallow AI wrappers with no proprietary state, wedge, or moat."
    )
    reject_pure_quota: bool = Field(
        default=True,
        description="Reject pure cold outbound bag-carrying or narrow execution silos."
    )
    min_timezone_overlap_hours: float = Field(
        default=4.0,
        description="Minimum domestic/regional timezone overlap required for effective sync."
    )


class IdealEmployerProfile(BaseModel):
    model_config = ConfigDict(extra="ignore")

    profile_name: str = Field(
        default="Operator Fit Profile",
        description="Descriptive name for the candidate or role target."
    )
    version: str = Field(
        default="1.0.0",
        description="Profile specification version."
    )
    wedge_capabilities: List[str] = Field(
        default_factory=lambda: [
            "0-to-1 go-to-market systems & field architecture",
            "Technical translation & buyer conviction",
            "Challenger positioning against legacy incumbents",
            "AI-assisted systems and platform development"
        ],
        description="Core high-leverage capabilities the candidate deploys."
    )
    required_stack: List[str] = Field(
        default_factory=lambda: ["Python", "PostgreSQL", "Kafka", "Modern Cloud / Kubernetes"],
        description="Core technical stack or infrastructure required in the target company."
    )
    negative_stack: List[str] = Field(
        default_factory=lambda: ["Legacy Mainframe", "Salesforce Apex Only"],
        description="Technologies indicating legacy bloat or misaligned engineering culture."
    )
    dealbreakers: Dealbreakers = Field(
        default_factory=Dealbreakers,
        description="Hard dealbreakers that disqualify companies immediately."
    )
    hiring_catalysts: List[str] = Field(
        default_factory=lambda: [
            "Scaling beyond founder-led sales",
            "Architectural migration off legacy systems",
            "Hitting throughput or latency limits on existing platform"
        ],
        description="Catalyst events that create urgent leadership budget and mandate."
    )
    target_leadership: List[str] = Field(
        default_factory=lambda: [
            "Low-ego technical founders",
            "Engineering-led founders seeking blunt commercial truth",
            "Hands-on technical builders"
        ],
        description="Leadership traits and counterpart profiles."
    )
    anchor_companies: List[str] = Field(
        default_factory=lambda: ["Stripe", "Linear", "Tailscale"],
        description="Exemplar companies that define the ideal engineering and operational culture."
    )

    @classmethod
    def load(cls, path: Path | str) -> IdealEmployerProfile:
        p = Path(path)
        if not p.exists():
            raise FileNotFoundError(f"Profile file not found at: {p}")
        data = json.loads(p.read_text(encoding="utf-8"))
        return cls.model_validate(data)

    def save(self, path: Path | str) -> None:
        p = Path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(self.model_dump_json(indent=2), encoding="utf-8")

    def to_triage_criteria(self) -> Dict[str, Any]:
        """Produce structured criteria dictionary for Lane 2 Gatekeeper Triage."""
        return {
            "max_headcount": self.dealbreakers.max_headcount,
            "policy": self.dealbreakers.policy,
            "disallowed_locations": self.dealbreakers.disallowed_locations,
            "reject_thin_wrappers": self.dealbreakers.reject_thin_wrappers,
            "reject_pure_quota": self.dealbreakers.reject_pure_quota,
        }

    def to_evaluation_prompt(self) -> str:
        """Render evaluation rubric for Lane 3 & 4 LLM analysis."""
        caps = "\n".join(f"- {c}" for c in self.wedge_capabilities)
        catalysts = "\n".join(f"- {c}" for c in self.hiring_catalysts)
        leaders = "\n".join(f"- {c}" for c in self.target_leadership)
        return (
            f"EVALUATION CRITERIA: {self.profile_name} (v{self.version})\n\n"
            f"1. CORE WEDGE CAPABILITIES:\n{caps}\n\n"
            f"2. HIRING CATALYSTS (URGENT PAIN):\n{catalysts}\n\n"
            f"3. LEADERSHIP & CULTURE REQUIREMENTS:\n{leaders}\n\n"
            f"EVIDENCE REQUIREMENT: Every positive claim MUST reference exact verbatim quotes "
            f"from captured source text. Never invent budget, traction, or role suitability."
        )
