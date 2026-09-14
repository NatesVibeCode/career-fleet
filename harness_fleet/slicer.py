"""Document text slicer preserving character offsets and grounding boundaries."""
import re
from typing import Any

# Sentence/paragraph boundaries we prefer to cut at: runs of terminal
# punctuation or newlines, plus any trailing quotes/brackets and whitespace.
_SENTENCE_END = re.compile(r"[.!?\n]+[\"')\]]*\s+")


def _snap_end(text: str, start: int, end: int) -> int:
    """Snap a hard window end back to the last sentence boundary.

    Only snaps within the trailing half of the window so windows keep
    making progress; falls back to the hard cut when no boundary qualifies.
    """
    if end >= len(text) or end - start < 2:
        return end
    floor = start + (end - start) // 2
    best = -1
    for match in _SENTENCE_END.finditer(text, start, end):
        if match.end() > floor:
            best = match.end()
    if best > start:
        return min(best, len(text))
    return end


def slice_document(
    text: str,
    max_chars: int = 6000,
    overlap_chars: int = 600,
) -> list[dict[str, Any]]:
    """Slices a text document into verifiable sections with offset tracking.

    If text length is <= max_chars, returns a single complete section.
    Otherwise yields lossless overlapping sliding windows: every character
    of the input appears in at least one slice, consecutive slices overlap
    by up to ``overlap_chars`` (capped below ``max_chars`` so iteration
    always advances), and window ends snap back to sentence boundaries to
    avoid cutting evidence mid-sentence. Offsets stay exact:
    ``text[start:end] == slice["text"]`` for every slice.
    """
    text = text or ""
    n = len(text)
    if n <= max_chars:
        return [{
            "slice_id": "full",
            "start": 0,
            "end": n,
            "text": text,
            "partial": False
        }]

    if max_chars < 1:
        raise ValueError("max_chars must be greater than 0")
    # Overlap scales with window size so degenerate widths (e.g. max_chars=2)
    # advance instead of crawling one char at a time; explicit overlap_chars
    # still caps the default 6000-char window at 600.
    overlap = max(0, min(int(overlap_chars), max_chars // 4)) if max_chars > 1 else 0

    slices: list[dict[str, Any]] = []
    start = 0
    index = 0
    while start < n:
        end = min(n, start + max_chars)
        end = _snap_end(text, start, end)
        if end <= start:  # degenerate snap; fall back to the hard cut
            end = min(n, start + max_chars)
        slices.append({
            "slice_id": f"s{index}",
            "start": start,
            "end": end,
            "text": text[start:end],
            "partial": True,
        })
        if end >= n:
            break
        # Overlap keeps evidence spanning a cut fully inside one slice.
        # Starts stay exact (no forward snapping) so coverage is lossless.
        start = max(end - overlap, start + 1)
        index += 1

    return slices
