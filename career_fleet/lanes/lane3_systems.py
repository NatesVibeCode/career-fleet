"""Lane 3: Technical Systems Wedge & Defensibility Evaluation.
Evaluates surviving companies for proprietary technical moats, stateful architectures,
and mission-critical infrastructure vs. fragile commodity wrappers.
"""
from __future__ import annotations

import re
from typing import Any, Dict, List, Optional
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


SYSTEM_WEDGE_SIGNALS = [
    (re.compile(r"\b(?:database|distributed systems|storage engine|raft|consensus|kafka)\b", re.I), "Infrastructure & Data Systems"),
    (re.compile(r"\b(?:compiler|runtime|ebpf|kernel|parser|ast|bytecode)\b", re.I), "Systems & Compiler Architecture"),
    (re.compile(r"\b(?:workflow engine|state machine|durable execution|event-driven)\b", re.I), "Durable Orchestration"),
    (re.compile(r"\b(?:fintech|banking rails|hipaa|pci-dss|soc2|compliance engine)\b", re.I), "Regulated Core Rails"),
]


def score_technical_wedge(
    text: str,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Score technical wedge depth and extract supporting quotes."""
    matched_wedges = []
    quotes = []

    for pattern, label in SYSTEM_WEDGE_SIGNALS:
        match = pattern.search(text)
        if match:
            matched_wedges.append(label)
            start = max(0, match.start() - 30)
            end = min(len(text), match.end() + 30)
            quotes.append(text[start:end].strip())

    # Stack alignment check
    matched_stack = [s for s in profile.required_stack if re.search(r"\b" + re.escape(s) + r"\b", text, re.I)]

    score = min(1.0, (len(matched_wedges) * 0.3) + (len(matched_stack) * 0.15))
    verdict = "HIGH FIT" if score >= 0.7 else ("STRONG FIT" if score >= 0.4 else "MARGINAL")

    return {
        "score": round(score, 2),
        "verdict": verdict,
        "matched_wedges": matched_wedges,
        "matched_stack": matched_stack,
        "quotes": quotes[:5],
        "rationale": f"Identified technical wedges: {', '.join(matched_wedges) or 'None'}. Stack alignment: {', '.join(matched_stack) or 'None'}.",
    }


def run_lane3_systems(
    store: CareerStore,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Execute Lane 3 Technical Systems Wedge evaluation on triaged survivors."""
    survivors = [c for c in store.list_companies() if c["status"] in ("triaged", "discovered")]
    evaluated = 0

    for comp in survivors:
        cid = comp["id"]
        dossier = store.get_company_dossier(cid)
        postings = dossier.get("jobs", []) if dossier else []
        combined_text = "\n".join(p.get("raw_text", "") for p in postings)

        res = score_technical_wedge(combined_text, profile)
        status = "qualified" if res["score"] >= 0.4 else "marginal"

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
