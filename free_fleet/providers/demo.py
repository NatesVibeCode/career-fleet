"""Deterministic demo provider for offline quickstart — no network."""
from __future__ import annotations

import json
import re
import uuid
from typing import Any, Optional, Tuple

from .base import BaseProvider


def _fabricate_claims(claims_schema: dict) -> dict:
    """Create minimal valid claims satisfying the closed schema."""
    props = claims_schema.get("properties", {})
    required = claims_schema.get("required", [])
    claims: dict[str, Any] = {}
    # Generate for required first, then optionally include all props
    for name in required:
        spec = props.get(name, {})
        claims[name] = _value_for_spec(spec)
    # Include optional props too so demo looks richer, but not required for validation
    return claims


def _value_for_spec(spec: dict) -> Any:
    if "enum" in spec:
        return spec["enum"][0]
    t = spec.get("type")
    if t == "string":
        return "demo verified value"
    if t == "boolean":
        return True
    if t == "integer":
        return 0
    if t == "number":
        return 0.0
    if t == "array":
        items = spec.get("items", {})
        if items.get("type") == "string":
            return ["demo"]
        if items.get("type") == "integer":
            return [0]
        return []
    if t == "object":
        if spec.get("properties"):
            return {k: _value_for_spec(v) for k, v in spec["properties"].items()}
        return {}
    # fallback
    if spec.get("properties"):
        return {k: _value_for_spec(v) for k, v in spec["properties"].items()}
    return "demo"


class DemoProvider(BaseProvider):
    """Deterministic offline provider that copies verbatim slices for quote grounding."""

    def run_prompt(
        self,
        route_id: str,
        prompt: str,
        system_prompt: Optional[str] = None,
        timeout_sec: int = 120,
        session_id: Optional[str] = None,
        policy: Optional[Any] = None,
    ) -> Tuple[bool, Optional[str], dict]:
        # Extract the task payload from the rendered prompt. Profile context
        # and other instructions may contain valid JSON objects before the
        # final task payload, so looking only at the first brace is ambiguous.
        payload = None
        decoder = json.JSONDecoder()
        cursor = 0
        while True:
            first_brace = prompt.find("{", cursor)
            if first_brace == -1:
                break
            try:
                candidate, _ = decoder.raw_decode(prompt[first_brace:])
            except json.JSONDecodeError:
                cursor = first_brace + 1
                continue
            if isinstance(candidate, dict) and {"input_items", "output_schema"}.issubset(candidate):
                payload = candidate
                break
            cursor = first_brace + 1
        if not payload or "input_items" not in payload or "output_schema" not in payload:
            receipt = {
                "id": f"demo-{uuid.uuid4().hex[:8]}",
                "session_id": session_id,
                "provider": "demo",
                "requested_route": route_id,
                "status": "failed",
                "cost": 0.0,
                "cost_status": "reported_zero",
                "usage": {"total_tokens": 0},
                "error": "demo provider could not parse prompt payload",
                "duration_seconds": 0.01,
            }
            return False, None, receipt

        output_schema = payload.get("output_schema", {})
        claims_schema = {}
        try:
            claims_schema = output_schema["properties"]["items"]["items"]["properties"]["claims"]
        except Exception:
            claims_schema = {"type": "object", "properties": {"summary": {"type": "string"}}, "required": ["summary"]}

        items_out = []
        for itm in payload.get("input_items", []):
            item_id = itm.get("item_id", f"item-{uuid.uuid4().hex[:4]}")
            sections = itm.get("sections") or itm.get("slices") or []
            if not sections:
                sections = [{"slice_id": "full", "text": ""}]
            # Pick first section with enough text for a quote
            quote_text = ""
            quote_slice_id = sections[0].get("slice_id", "full")
            for sec in sections:
                txt = sec.get("text", "")
                if len(txt.strip()) >= 15:
                    # take first 40 chars or full if shorter, ensure verbatim
                    snippet = txt.strip()[:60].strip()
                    # ensure at least 15 chars
                    if len(snippet) >= 15:
                        quote_text = snippet
                        quote_slice_id = sec.get("slice_id", "full")
                        break
            if not quote_text:
                candidate_quote = sections[0].get("text", "")
                if len(candidate_quote.strip()) >= 15:
                    quote_text = candidate_quote.strip()[:60].strip()
                else:
                    quote_text = "demo verified value fallback quote"
            claims = _fabricate_claims(claims_schema)
            items_out.append({
                "item_id": item_id,
                "claims": claims,
                "quotes": [{"slice_id": quote_slice_id, "text": quote_text}],
            })

        response = json.dumps({"items": items_out}, ensure_ascii=False)
        receipt = {
            "id": f"demo-{uuid.uuid4().hex[:8]}",
            "session_id": session_id,
            "provider": "demo",
            "requested_route": route_id,
            "status": "complete",
            "cost": 0.0,
            "cost_status": "reported_zero",
            "usage": {"total_tokens": 10},
            "error": None,
            "duration_seconds": 0.005,
        }
        return True, response, receipt
