"""Deterministic evidence-candidate spans.

The worker model no longer free-searches source text for quotes. Code ranks
sentences by evidence-term overlap and numbers the winners; the model cites a
candidate id and copies its text. Candidate ids are reproducible from
(section text, terms, top_n) alone, so verification recomputes them instead
of trusting model offsets. This module is pure: no store, no network, no
model calls.
"""
from __future__ import annotations

import re
from typing import Any

_SENTENCE_RE = re.compile(r"[^.!?\n]+[.!?]?(?=\s|$)")
MIN_SPAN_CHARS = 15
MAX_SPAN_CHARS = 300
MAX_TERMS = 64


def split_sentences(text: str) -> list[tuple[int, int, str]]:
    """Split text into (start, end, sentence) spans with exact offsets."""
    spans: list[tuple[int, int, str]] = []
    for match in _SENTENCE_RE.finditer(text):
        start, end = match.start(), match.end()
        while start < end and text[start].isspace():
            start += 1
        while end > start and text[end - 1].isspace():
            end -= 1
        if start < end:
            spans.append((start, end, text[start:end]))
    return spans


def normalize_terms(terms: list[str] | tuple[str, ...] | None) -> list[str]:
    """Lowercase, strip, dedupe terms while preserving order."""
    seen: set[str] = set()
    clean: list[str] = []
    for term in terms or []:
        if not isinstance(term, str):
            continue
        folded = term.strip().casefold()
        if folded and folded not in seen:
            seen.add(folded)
            clean.append(folded)
    return clean[:MAX_TERMS]


def merge_terms(*term_lists: list[str] | tuple[str, ...] | None) -> list[str]:
    """Union several term lists, preserving first-seen order."""
    merged: list[str] = []
    for terms in term_lists:
        for term in normalize_terms(terms):
            if term not in merged:
                merged.append(term)
    return merged[:MAX_TERMS]


def extract_candidates(
    slice_text: str,
    terms: list[str] | tuple[str, ...] | None,
    top_n: int = 6,
    max_span_chars: int = MAX_SPAN_CHARS,
) -> list[dict[str, Any]]:
    """Rank sentences by distinct term overlap; return numbered spans.

    Each span is {"candidate_id", "start", "end", "text"} with offsets
    relative to slice_text. Ranking is deterministic: more distinct term
    hits win, ties break by earlier position, ids follow position order.
    Returns [] when no terms match (the caller then falls back to the
    legacy free-search quote path).
    """
    wanted = normalize_terms(terms)
    if not wanted or not slice_text:
        return []
    scored: list[tuple[int, int, int, str]] = []
    for start, end, sentence in split_sentences(slice_text):
        if len(sentence) < MIN_SPAN_CHARS or len(sentence) > max_span_chars:
            continue
        lowered = sentence.casefold()
        hits = sum(1 for term in wanted if term in lowered)
        if hits:
            scored.append((hits, start, end, sentence))
    scored.sort(key=lambda row: (-row[0], row[1]))
    chosen = sorted(scored[: max(0, top_n)], key=lambda row: row[1])
    return [
        {"candidate_id": index, "start": start, "end": end, "text": sentence}
        for index, (_, start, end, sentence) in enumerate(chosen)
    ]


def profile_evidence_terms(profile: Any) -> list[str]:
    """Collect evidence terms from a qualification profile, duck-typed.

    Reads the list/str attributes profiles use for stack, triggers, roles,
    and category context. Unknown profile shapes yield [] rather than
    raising: candidates are an optimization, never a requirement.
    """
    terms: list[str] = []
    for attr in (
        "required_stack",
        "trigger_pain_phrases",
        "target_roles",
        "negative_stack_exclusions",
        "anchor_logos",
        "product_category",
        "architectural_layer",
    ):
        try:
            value = getattr(profile, attr, None)
        except Exception:
            continue
        if isinstance(value, str):
            value = [value]
        if not isinstance(value, (list, tuple)):
            continue
        for term in value:
            if isinstance(term, str) and term.strip():
                terms.append(term.strip())
    return normalize_terms(terms)


def attach_candidates(
    items: list[dict[str, Any]],
    terms: list[str] | tuple[str, ...] | None,
    top_n: int = 6,
) -> list[dict[str, Any]]:
    """Return prompt item dicts with per-section candidate spans attached.

    Input dicts are not mutated. Sections without term matches carry an
    empty candidate list, which tells the worker to quote freely there.
    """
    wanted = normalize_terms(terms)
    attached: list[dict[str, Any]] = []
    for item in items:
        section_list = item.get("sections") or []
        new_sections = []
        for section in section_list:
            text = section.get("text", "") if isinstance(section, dict) else ""
            spans = extract_candidates(text, wanted, top_n) if text else []
            new_section = dict(section) if isinstance(section, dict) else {}
            new_section["candidates"] = spans
            new_sections.append(new_section)
        new_item = dict(item)
        new_item["sections"] = new_sections
        attached.append(new_item)
    return attached
