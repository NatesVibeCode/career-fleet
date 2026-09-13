"""Document text slicer preserving character offsets and grounding boundaries."""
from typing import Dict, List, Any

def slice_document(text: str, max_chars: int = 6000) -> List[Dict[str, Any]]:
    """Slices a text document into verifiable sections with offset tracking.
    
    If text length is <= max_chars, returns a single complete section.
    If longer, captures the beginning (head), middle, and end (tail) with exact
    character offsets, marking partial_input=True.
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
    
    # 3-window slice (head, mid, tail) - lossless sliding is planned as opt-in via --sliding flag;
    # current deterministic tri-window preserves existing test contracts and audit offsets.
    width = max(1, max_chars // 3)
    head = {"slice_id": "head", "start": 0, "end": width, "text": text[0:width], "partial": True}
    
    mid_start = (n - width) // 2
    mid = {"slice_id": "mid", "start": mid_start, "end": mid_start + width, "text": text[mid_start:mid_start + width], "partial": True}
    
    tail_start = n - width
    tail = {"slice_id": "tail", "start": tail_start, "end": n, "text": text[tail_start:n], "partial": True}
    
    return [head, mid, tail]
