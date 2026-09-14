"""Deterministic demo provider for offline quickstart — no network."""
from __future__ import annotations

import json
import uuid
from typing import Any

from ..models import ProviderReceipt, RoutePolicy
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
    if "score" in claims and "fit_tier" in claims:
        # Tier is derived from score, not fabricated: keep demo output
        # consistent with TaskSpec.validate_claims.
        from ..models import score_to_fit_tier

        try:
            claims["fit_tier"] = score_to_fit_tier(claims["score"])
        except ValueError:
            pass
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
        system_prompt: str | None = None,
        timeout_sec: int = 120,
        session_id: str | None = None,
        policy: RoutePolicy | None = None,
    ) -> tuple[bool, str | None, ProviderReceipt]:
        # Extract the task payload from the rendered prompt. Profile context
        # and other instructions may contain valid JSON objects before the
        # final task payload, so looking only at the first brace is ambiguous.
        from .base import extract_task_payload

        payload = extract_task_payload(prompt)
        if not payload or "input_items" not in payload or "output_schema" not in payload:
            receipt: ProviderReceipt = ProviderReceipt(
                id=f"demo-{uuid.uuid4().hex[:8]}",
                session_id=session_id,
                provider="demo",
                requested_route=route_id,
                status="failed",
                cost=0.0,
                cost_status="reported_zero",
                usage={"total_tokens": 0},
                error="demo provider could not parse prompt payload",
                duration_seconds=0.01,
            )
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
            quote = {"slice_id": quote_slice_id, "text": quote_text}
            # Checklist tasks need linked evidence: the single demo quote
            # backs every fabricated true answer so coverage holds offline.
            checklist = claims.get("checklist")
            if isinstance(checklist, dict):
                quote["supports"] = sorted(str(key) for key, value in checklist.items() if value is True)
            # A deterministic prefix can occur more than once in a long source
            # (for example when a page repeats its title).  Include exact
            # absolute offsets so grounding can disambiguate it instead of
            # rejecting an otherwise valid offline/demo response.
            for sec in sections:
                if sec.get("slice_id", "full") != quote_slice_id:
                    continue
                sec_text = sec.get("text", "")
                rel_start = sec_text.find(quote_text)
                if rel_start >= 0:
                    abs_start = int(sec.get("start", 0)) + rel_start
                    quote["start"] = abs_start
                    quote["end"] = abs_start + len(quote_text)
                break
            items_out.append({
                "item_id": item_id,
                "claims": claims,
                "quotes": [quote],
            })

        response = json.dumps({"items": items_out}, ensure_ascii=False)
        receipt = ProviderReceipt(
            id=f"demo-{uuid.uuid4().hex[:8]}",
            session_id=session_id,
            provider="demo",
            requested_route=route_id,
            status="complete",
            cost=0.0,
            cost_status="reported_zero",
            usage={"total_tokens": 10},
            error=None,
            duration_seconds=0.005,
        )
        return True, response, receipt
