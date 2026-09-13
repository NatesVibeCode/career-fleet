"""Lane 4: Founder & Operational Culture Recon.
Evaluates leadership pedigree, technical humility, team geography (domestic vs offshore sync friction),
and communication culture.
"""
from __future__ import annotations

import re
from typing import Any, Dict, List, Optional
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


CULTURE_POSITIVES = [
    (re.compile(r"\b(?:technical founder|engineer-led|builder-first)\b", re.I), "Engineering-Led Leadership"),
    (re.compile(r"\b(?:intellectual honesty|high agency|low ego|blunt feedback)\b", re.I), "High Agency & Low Ego"),
    (re.compile(r"\b(?:async-first|transparent|written culture)\b", re.I), "Async Written Culture"),
]

CULTURE_CONCERNS = [
    (re.compile(r"\b(?:fast-paced family|wear many hats without equity|rockstar)\b", re.I), "Vague Burnout Culture"),
    (re.compile(r"\b(?:strict 8am sync|monitoring software|time-tracking)\b", re.I), "Micromanagement"),
]


def score_culture_and_team(
    text: str,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Score operational culture, founder traits, and distribution."""
    if not text or not text.strip():
        return {
            "score": 0.0,
            "verdict": "UNKNOWN",
            "positives": [],
            "concerns": [],
            "quotes": [],
            "rationale": "No captured source text available to evaluate operational culture.",
        }

    positives = []
    concerns = []
    quotes = []

    for pattern, label in CULTURE_POSITIVES:
        match = pattern.search(text)
        if match:
            positives.append(label)
            start = max(0, match.start() - 25)
            end = min(len(text), match.end() + 25)
            quotes.append(text[start:end].strip())

    for pattern, label in CULTURE_CONCERNS:
        match = pattern.search(text)
        if match:
            concerns.append(label)
            start = max(0, match.start() - 25)
            end = min(len(text), match.end() + 25)
            quotes.append(text[start:end].strip())

    base_score = 0.5 + (len(positives) * 0.2) - (len(concerns) * 0.3)
    score = max(0.0, min(1.0, base_score))
    verdict = "HEALTHY" if score >= 0.7 else ("ACCEPTABLE" if score >= 0.4 else "CONCERN")

    return {
        "score": round(score, 2),
        "verdict": verdict,
        "positives": positives,
        "concerns": concerns,
        "quotes": quotes[:5],
        "rationale": f"Cultural signals: {', '.join(positives) or 'Neutral'}. Concerns: {', '.join(concerns) or 'None'}.",
    }


def run_lane4_culture(
    store: CareerStore,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Execute Lane 4 Culture & Leadership evaluation on qualified targets."""
    qualified = [c for c in store.list_companies() if c["status"] in ("qualified", "triaged")]
    evaluated = 0

    for comp in qualified:
        cid = comp["id"]
        dossier = store.get_company_dossier(cid)
        postings = dossier.get("jobs", []) if dossier else []
        combined_text = "\n".join(p.get("raw_text", "") for p in postings)

        res = score_culture_and_team(combined_text, profile)

        store.record_evaluation(
            eval_id=f"eval-culture-{cid}",
            company_id=cid,
            lane="lane4_culture",
            status="qualified" if res["score"] >= 0.4 else "marginal",
            score=res["score"],
            verdict=res["verdict"],
            rationale=res["rationale"],
            quotes=res["quotes"],
            model_used="deterministic-culture-scorer",
        )
        evaluated += 1

    return {
        "status": "success",
        "evaluated": evaluated,
    }
