"""Lane 2: Gatekeeper Triage.
Rapidly filters candidate companies against hard dealbreakers:
- Excessive headcount (> 80-100 people)
- Mandatory non-local in-person office mandates (SF/NYC 4-5 day office policies)
- Shallow prompt-wrapper architecture
Drops disqualified companies immediately to save time and tokens.
"""
from __future__ import annotations

import re
from typing import Any, Dict, List, Optional
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


OFFICE_MANDATE_PATTERNS = [
    re.compile(r"\b(?:5|4)\s*days?\s*(?:a\s*week\s*)?(?:in\s*(?:the\s*|our\s*)?(?:\w+\s*)?office|on[- ]?site)\b", re.I),
    re.compile(r"\bmandatory\s*(?:in[- ]?office|in[- ]?person|presence)\b", re.I),
    re.compile(r"\b(?:san\s*francisco|sf|new\s*york|nyc)\s*(?:office|in[- ]?person)\s*(?:required|mandatory)\b", re.I),
    re.compile(r"\bno\s*remote\s*(?:option|work|allowed)\b", re.I),
]

WRAPPER_PATTERNS = [
    re.compile(r"\bwrapper\s*around\s*(?:chatgpt|openai)\b", re.I),
    re.compile(r"\bprompt\s*engineering\s*agency\b", re.I),
    re.compile(r"\bai\s*sdr\s*(?:spammer|cold\s*email\s*generator)\b", re.I),
]


def check_dealbreakers(
    company: Dict[str, Any],
    postings: List[Dict[str, Any]],
    profile: IdealEmployerProfile,
) -> Optional[Dict[str, Any]]:
    """Return disqualification dictionary if any hard dealbreaker triggers, else None."""
    dealbreakers = profile.dealbreakers

    # 1. Headcount check
    headcount = company.get("headcount")
    if headcount and dealbreakers.max_headcount and headcount > dealbreakers.max_headcount:
        return {
            "disqualified": True,
            "reason": f"Headcount ({headcount}) exceeds maximum threshold ({dealbreakers.max_headcount})",
            "rule": "headcount_limit",
        }

    # 2. In-person mandate check across job postings
    combined_text = "\n".join(p.get("raw_text", "") for p in postings)
    if dealbreakers.policy == "remote_only":
        for pat in OFFICE_MANDATE_PATTERNS:
            match = pat.search(combined_text)
            if match:
                matched_snippet = combined_text[max(0, match.start() - 40) : min(len(combined_text), match.end() + 40)]
                return {
                    "disqualified": True,
                    "reason": f"Mandatory in-office policy detected: '{matched_snippet.strip()}'",
                    "rule": "office_mandate",
                    "quote": match.group(0),
                }

    # 3. Shallow wrapper check
    if dealbreakers.reject_thin_wrappers:
        for pat in WRAPPER_PATTERNS:
            match = pat.search(combined_text)
            if match:
                return {
                    "disqualified": True,
                    "reason": f"Shallow AI wrapper signal detected: '{match.group(0)}'",
                    "rule": "thin_wrapper",
                    "quote": match.group(0),
                }

    return None


def run_lane2_triage(
    store: CareerStore,
    profile: IdealEmployerProfile,
) -> Dict[str, Any]:
    """Execute Gatekeeper Triage across all discovered companies."""
    discovered = store.list_companies(status="discovered")
    triaged = 0
    passed = 0
    dropped = 0

    for comp in discovered:
        cid = comp["id"]
        dossier = store.get_company_dossier(cid)
        postings = dossier.get("jobs", []) if dossier else []

        dq = check_dealbreakers(comp, postings, profile)
        if dq:
            store.record_evaluation(
                eval_id=f"eval-triage-{cid}",
                company_id=cid,
                lane="lane2_triage",
                status="disqualified",
                score=0.0,
                verdict="DISQUALIFIED",
                rationale=dq["reason"],
                quotes=[dq.get("quote")] if dq.get("quote") else [],
            )
            dropped += 1
        else:
            store.record_evaluation(
                eval_id=f"eval-triage-{cid}",
                company_id=cid,
                lane="lane2_triage",
                status="triaged",
                score=1.0,
                verdict="SURVIVOR",
                rationale="Passed all deterministic gatekeeper dealbreaker checks.",
            )
            passed += 1
        triaged += 1

    return {
        "status": "success",
        "total_triaged": triaged,
        "survivors": passed,
        "disqualified": dropped,
    }
