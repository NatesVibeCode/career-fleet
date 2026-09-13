"""Lane 2: Gatekeeper Triage.
Rapidly filters candidate companies against hard dealbreakers:
- Excessive headcount (> 80-100 people)
- Mandatory non-local in-person office mandates (SF/NYC 4-5 day office policies)
- Shallow prompt-wrapper architecture
Drops disqualified companies immediately to save time and tokens.
"""
from __future__ import annotations

from datetime import datetime, timezone as dt_timezone
import re
from typing import Any, Dict, List, Optional
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


OFFICE_MANDATE_PATTERNS = [
    re.compile(r"\b(?:5|4)\s*days?\s*(?:a\s*week\s*)?(?:in\s*(?:the\s*|our\s*)?(?:\w+\s*)?office|on[- ]?site)\b", re.I),
    re.compile(r"\bmandatory\s*(?:in[- ]?office|in[- ]?person|presence)\b", re.I),
    re.compile(r"\b(?:san\s*francisco|sf|new\s*york|nyc)\s*(?:office|in[- ]?person)\s*(?:required|mandatory)\b", re.I),
    re.compile(r"\bno\s*remote\s*(?:option|work|allowed)\b", re.I),
    re.compile(r"\b(?:office|on[- ]?site|in[- ]?person|in[- ]?office)\b[^.\n]{0,60}\b(?:required|mandatory|must)\b", re.I),
    re.compile(r"\b(?:required|mandatory|must)\b[^.\n]{0,60}\b(?:office|on[- ]?site|in[- ]?person|in[- ]?office)\b", re.I),
]

REMOTE_ONLY_DAYS_PATTERN = re.compile(
    r"\b(?:[1-5])\s*days?\s*(?:a\s*week|per\s*week|each\s*week)?\s*"
    r"(?:in\s*(?:the\s*|our\s*)?(?:\w+\s*)?office|on[- ]?site)\b",
    re.I,
)

WRAPPER_PATTERNS = [
    re.compile(r"\bwrapper\s*around\s*(?:chatgpt|openai)\b", re.I),
    re.compile(r"\bprompt\s*engineering\s*agency\b", re.I),
    re.compile(r"\bai\s*sdr\s*(?:spammer|cold\s*email\s*generator)\b", re.I),
]

QUOTA_PATTERNS = [
    re.compile(r"\b(?:pure\s+quota|quota[- ]only|quota[- ]carrying|cold[- ]calling|cold[- ]outbound|boiler[- ]room)\b", re.I),
]

REMOTE_NEGATION_PATTERNS = [
    re.compile(r"\b(?:no|not|without)\s+(?:fully\s+)?remote\b", re.I),
    re.compile(r"\bremote\s+(?:work\s+)?(?:is\s+)?(?:not\s+)?(?:required|allowed|available|permitted|optional)\b", re.I),
]

REMOTE_POSITIVE_PATTERN = re.compile(
    r"\b(?:fully|100%|completely|entirely)?\s*remote\b|"
    r"\bremote[- ]first\b|\bwork\s+from\s+anywhere\b",
    re.I,
)

LOCATION_STOPWORDS = {
    "a", "an", "and", "at", "day", "days", "each", "in", "mandate", "mandatory",
    "of", "office", "on", "onsite", "on-site", "per", "presence", "required", "site",
    "the", "week", "with",
}


def _screening_text(company: Dict[str, Any], postings: List[Dict[str, Any]]) -> str:
    """Combine source text and structured location metadata for screening."""
    parts = [str(company.get("hq_location") or "")]
    for posting in postings:
        parts.extend(
            [
                str(posting.get("raw_text") or ""),
                str(posting.get("location") or ""),
            ]
        )
    return "\n".join(part for part in parts if part.strip())


def _location_matches(text: str, configured_location: str) -> bool:
    """Match the meaningful location words, ignoring policy words."""
    terms = [
        token
        for token in re.findall(r"[A-Za-z0-9]+", configured_location.casefold())
        if token not in LOCATION_STOPWORDS
    ]
    return bool(terms) and all(re.search(r"\b" + re.escape(term) + r"\b", text, re.I) for term in terms)


def _has_remote_evidence(posting: Dict[str, Any]) -> bool:
    text = " ".join(str(posting.get(key) or "") for key in ("raw_text", "location"))
    if any(pattern.search(text) for pattern in REMOTE_NEGATION_PATTERNS):
        return False
    return bool(posting.get("is_remote")) or bool(REMOTE_POSITIVE_PATTERN.search(text))


def _business_day_overlap_hours(candidate_timezone: str, employer_timezone: str) -> float:
    """Calculate overlap for a standard 09:00–17:00 local workday."""
    try:
        candidate_zone = ZoneInfo(candidate_timezone)
        employer_zone = ZoneInfo(employer_timezone)
    except ZoneInfoNotFoundError as exc:
        raise ValueError(f"unknown IANA timezone: {exc}") from exc

    reference = datetime(2026, 1, 15, 12, tzinfo=dt_timezone.utc)
    candidate_offset = reference.astimezone(candidate_zone).utcoffset()
    employer_offset = reference.astimezone(employer_zone).utcoffset()
    if candidate_offset is None or employer_offset is None:
        return 0.0

    candidate_start = 9.0 - candidate_offset.total_seconds() / 3600
    candidate_end = 17.0 - candidate_offset.total_seconds() / 3600
    employer_start = 9.0 - employer_offset.total_seconds() / 3600
    employer_end = 17.0 - employer_offset.total_seconds() / 3600
    return max(0.0, min(candidate_end, employer_end) - max(candidate_start, employer_start))


def _check_timezone_overlap(
    company: Dict[str, Any],
    postings: List[Dict[str, Any]],
    profile: IdealEmployerProfile,
) -> Optional[Dict[str, Any]]:
    dealbreakers = profile.dealbreakers
    candidate_timezone = dealbreakers.candidate_timezone
    if not candidate_timezone:
        return None

    employer_timezones = [str(company.get("timezone") or "").strip()]
    employer_timezones.extend(str(posting.get("timezone") or "").strip() for posting in postings)
    employer_timezones = [value for value in employer_timezones if value]
    if not employer_timezones:
        return {
            "disqualified": True,
            "reason": "Timezone overlap could not be verified because the captured source has no timezone metadata.",
            "rule": "timezone_overlap_unknown",
        }

    overlaps = []
    for employer_timezone in employer_timezones:
        try:
            overlaps.append(_business_day_overlap_hours(candidate_timezone, employer_timezone))
        except ValueError as exc:
            return {
                "disqualified": True,
                "reason": str(exc),
                "rule": "invalid_timezone",
            }

    best_overlap = max(overlaps)
    if best_overlap < dealbreakers.min_timezone_overlap_hours:
        return {
            "disqualified": True,
            "reason": (
                f"Best business-hour timezone overlap ({best_overlap:.1f}h) is below the required "
                f"{dealbreakers.min_timezone_overlap_hours:.1f}h."
            ),
            "rule": "timezone_overlap",
            "quote": employer_timezones[overlaps.index(best_overlap)],
        }
    return None


def check_dealbreakers(
    company: Dict[str, Any],
    postings: List[Dict[str, Any]],
    profile: IdealEmployerProfile,
) -> Optional[Dict[str, Any]]:
    """Return disqualification dictionary if any hard dealbreaker triggers, else None."""
    dealbreakers = profile.dealbreakers

    # 1. Headcount check
    headcount = company.get("headcount")
    if headcount is not None and dealbreakers.max_headcount is not None:
        try:
            hc_int = int(headcount)
            if hc_int > dealbreakers.max_headcount:
                return {
                    "disqualified": True,
                    "reason": f"Headcount ({hc_int}) exceeds maximum threshold ({dealbreakers.max_headcount})",
                    "rule": "headcount_limit",
                }
        except (ValueError, TypeError):
            pass

    # 2. In-person mandate check across job postings and structured metadata.
    combined_text = _screening_text(company, postings)
    if dealbreakers.policy in ("remote_only", "remote_or_hybrid"):
        office_patterns = (
            [REMOTE_ONLY_DAYS_PATTERN, *OFFICE_MANDATE_PATTERNS]
            if dealbreakers.policy == "remote_only"
            else OFFICE_MANDATE_PATTERNS
        )
        for pat in office_patterns:
            match = pat.search(combined_text)
            if match:
                matched_snippet = combined_text[max(0, match.start() - 40) : min(len(combined_text), match.end() + 40)]
                return {
                    "disqualified": True,
                    "reason": f"Mandatory in-office policy detected: '{matched_snippet.strip()}'",
                    "rule": "office_mandate",
                    "quote": match.group(0),
                }

        if dealbreakers.policy == "remote_only":
            for posting in postings:
                location = str(posting.get("location") or "").strip()
                if location and not _has_remote_evidence(posting):
                    return {
                        "disqualified": True,
                        "reason": f"Remote-only policy conflicts with posting location '{location}'.",
                        "rule": "non_remote_location",
                        "quote": location,
                    }

    # Configured location exclusions are hard exclusions even when the user
    # allows other kinds of work arrangements.
    for configured_location in dealbreakers.disallowed_locations:
        if _location_matches(combined_text, configured_location):
            match = next((pat.search(combined_text) for pat in OFFICE_MANDATE_PATTERNS if pat.search(combined_text)), None)
            mandate_language = re.search(r"\b(?:required|mandatory|must)\b", combined_text, re.I)
            if match or mandate_language:
                quote = match.group(0) if match else mandate_language.group(0)
                return {
                    "disqualified": True,
                    "reason": f"Mandatory work location matches configured exclusion '{configured_location}': '{quote}'",
                    "rule": "configured_location",
                    "quote": quote,
                }

    if dealbreakers.policy == "remote_only" and (
        not postings or not any(_has_remote_evidence(posting) for posting in postings)
    ):
        return {
            "disqualified": True,
            "reason": "Remote-only policy could not be verified from the captured source.",
            "rule": "remote_policy_unknown",
        }

    timezone_issue = _check_timezone_overlap(company, postings, profile)
    if timezone_issue:
        return timezone_issue

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

    # 4. Pure quota / boiler-room check
    if dealbreakers.reject_pure_quota:
        for pat in QUOTA_PATTERNS:
            match = pat.search(combined_text)
            if match:
                return {
                    "disqualified": True,
                    "reason": f"Pure quota-sales signal detected: '{match.group(0)}'",
                    "rule": "pure_quota",
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
