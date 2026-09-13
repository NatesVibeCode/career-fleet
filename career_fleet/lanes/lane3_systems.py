"""Lane 3: Technical Systems Wedge & Defensibility Evaluation.
Evaluates surviving companies for proprietary technical moats, stateful architectures,
and mission-critical infrastructure vs. fragile commodity wrappers.
"""
from __future__ import annotations

import re
from typing import Any, Dict, List
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


SYSTEM_WEDGE_SIGNALS = [
    (re.compile(r"\b(?:database|distributed systems|storage engine|raft|consensus|kafka)\b", re.I), "Infrastructure & Data Systems"),
    (re.compile(r"\b(?:compiler|runtime|ebpf|kernel|parser|ast|bytecode)\b", re.I), "Systems & Compiler Architecture"),
    (re.compile(r"\b(?:workflow engine|state machine|durable execution|event-driven)\b", re.I), "Durable Orchestration"),
    (re.compile(r"\b(?:fintech|banking rails|hipaa|pci-dss|soc2|compliance engine)\b", re.I), "Regulated Core Rails"),
]


def _phrase_pattern(phrase: str) -> re.Pattern[str] | None:
    terms = re.findall(r"[A-Za-z0-9]+", phrase or "")
    if not terms:
        return None
    return re.compile(r"\b" + r"[\W_]+".join(re.escape(term) for term in terms) + r"\b", re.I)


def _configured_matches(text: str, phrases: List[str]) -> List[str]:
    matches = []
    for phrase in phrases:
        pattern = _phrase_pattern(phrase)
        if pattern and pattern.search(text):
            matches.append(phrase)
    return matches


def score_technical_wedge(
    text: str,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Score technical wedge depth and extract supporting quotes."""
    if not text or not text.strip():
        return {
            "score": 0.0,
            "verdict": "UNKNOWN",
            "matched_wedges": [],
            "matched_stack": [],
            "matched_capabilities": [],
            "matched_catalysts": [],
            "matched_negative_stack": [],
            "quotes": [],
            "rationale": "No captured source text available to evaluate technical wedge.",
        }

    matched_wedges = []
    quotes = []

    for pattern, label in SYSTEM_WEDGE_SIGNALS:
        match = pattern.search(text)
        if match:
            matched_wedges.append(label)
            start = max(0, match.start() - 30)
            end = min(len(text), match.end() + 30)
            quotes.append(text[start:end].strip())

    # Stack alignment check (handles slash-separated technologies like 'Modern Cloud / Kubernetes')
    matched_stack = []
    for s in profile.required_stack:
        parts = [p.strip() for p in s.split("/") if p.strip()]
        for part in parts:
            if re.search(r"\b" + re.escape(part) + r"\b", text, re.I) and part not in matched_stack:
                matched_stack.append(part)

    matched_capabilities = _configured_matches(text, profile.wedge_capabilities)
    matched_catalysts = _configured_matches(text, profile.hiring_catalysts)
    matched_negative_stack = _configured_matches(text, profile.negative_stack)

    for phrase in matched_capabilities + matched_catalysts + matched_negative_stack:
        pattern = _phrase_pattern(phrase)
        match = pattern.search(text) if pattern else None
        if match:
            start = max(0, match.start() - 30)
            end = min(len(text), match.end() + 30)
            quotes.append(text[start:end].strip())

    score = (
        (len(matched_wedges) * 0.3)
        + (len(matched_stack) * 0.15)
        + (len(matched_capabilities) * 0.1)
        + (len(matched_catalysts) * 0.1)
        - (len(matched_negative_stack) * 0.3)
    )
    score = max(0.0, min(1.0, score))
    verdict = "HIGH FIT" if score >= 0.8 else ("STRONG FIT" if score >= 0.6 else "MARGINAL")

    return {
        "score": round(score, 2),
        "verdict": verdict,
        "matched_wedges": matched_wedges,
        "matched_stack": matched_stack,
        "matched_capabilities": matched_capabilities,
        "matched_catalysts": matched_catalysts,
        "matched_negative_stack": matched_negative_stack,
        "quotes": quotes[:5],
        "rationale": (
            f"Identified technical wedges: {', '.join(matched_wedges) or 'None'}. "
            f"Stack alignment: {', '.join(matched_stack) or 'None'}. "
            f"Profile capability matches: {', '.join(matched_capabilities) or 'None'}. "
            f"Hiring catalyst matches: {', '.join(matched_catalysts) or 'None'}. "
            f"Negative stack signals: {', '.join(matched_negative_stack) or 'None'}."
        ),
    }


def run_lane3_systems(
    store: CareerStore,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Execute Lane 3 Technical Systems Wedge evaluation on triaged survivors."""
    survivors = [c for c in store.list_companies() if c["status"] == "triaged"]
    evaluated = 0

    for comp in survivors:
        cid = comp["id"]
        dossier = store.get_company_dossier(cid)
        postings = dossier.get("jobs", []) if dossier else []
        combined_text = "\n".join(p.get("raw_text", "") for p in postings)

        res = score_technical_wedge(combined_text, profile)
        status = "qualified" if res["score"] >= 0.6 else "marginal"

        store.record_evaluation(
            eval_id=f"eval-systems-{cid}",
            company_id=cid,
            lane="lane3_systems",
            status=status,
            score=res["score"],
            verdict=res["verdict"],
            rationale=res["rationale"],
            quotes=res["quotes"],
            model_used="deterministic-wedge-scorer",
        )
        evaluated += 1

    return {
        "status": "success",
        "evaluated": evaluated,
    }
