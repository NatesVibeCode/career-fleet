from __future__ import annotations

import os
import threading
import time
import uuid
from typing import Any

import httpx

from ..models import ProviderReceipt, RoutePolicy
from .base import BaseProvider

_shared_client: httpx.Client | None = None
_client_lock = threading.Lock()

def _shared_httpx_client(timeout: int = 120) -> httpx.Client:
    global _shared_client
    # Disable pooling under pytest to allow monkeypatch isolation per test
    if os.environ.get("PYTEST_CURRENT_TEST"):
        return httpx.Client(
            timeout=timeout,
            limits=httpx.Limits(max_keepalive_connections=10, max_connections=20),
            follow_redirects=True,
        )
    with _client_lock:
        # Invalidate cache if underlying httpx.Client was monkeypatched to a fake (e.g., FakeClient)
        if _shared_client is not None and not isinstance(_shared_client, httpx.Client):
            try:
                _shared_client.close()
            except Exception:
                pass
            _shared_client = None
        if _shared_client is None or _shared_client.is_closed:
            _shared_client = httpx.Client(
                timeout=timeout,
                limits=httpx.Limits(max_keepalive_connections=50, max_connections=100),
                http2=False,
                follow_redirects=True,
            )
        return _shared_client

def _should_use_ephemeral_client() -> bool:
    return bool(os.environ.get("PYTEST_CURRENT_TEST"))

def _extract_output_schema(prompt: str) -> dict | None:
    from .base import extract_task_payload

    try:
        payload = extract_task_payload(prompt)
        if isinstance(payload, dict) and "output_schema" in payload:
            return payload["output_schema"]
    except Exception:
        pass
    return None

class OpenRouterError(Exception):
    pass

class OpenRouterProvider(BaseProvider):
    def __init__(self, api_key: str | None = None, base_url: str = "https://openrouter.ai/api/v1"):
        self.api_key = api_key or os.environ.get("OPENROUTER_API_KEY", "")
        self.base_url = base_url.rstrip("/")

    def run_prompt(
        self,
        route_id: str,
        prompt: str,
        system_prompt: str | None = None,
        timeout_sec: int = 120,
        session_id: str | None = None,
        policy: RoutePolicy | None = None,
    ) -> tuple[bool, str | None, ProviderReceipt]:
        started = time.time()
        rid = uuid.uuid4().hex

        # Route id can be "openrouter/foo/bar:free" or "foo/bar:free"
        model_name = route_id.removeprefix("openrouter/").removeprefix("openrouter:")

        receipt: ProviderReceipt = ProviderReceipt(
            # openrouter constructs with string price_state; runtime verifies

            id=rid,
            session_id=session_id,
            provider="openrouter",
            requested_route=route_id,
            status="failed",
            cost=None,
            cost_status="unknown",
            usage=None,
            error=None,
            error_type=None,
            retry_after=None,
            duration_seconds=None,
        )

        if not self.api_key:
            receipt.error = "OPENROUTER_API_KEY is not set"
            receipt.error_type = "auth_error"
            receipt.duration_seconds = time.time() - started
            return False, None, receipt

        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "HTTP-Referer": "https://github.com/NatesVibeCode/harness-fleet",
            "X-Title": "harness-fleet",
        }

        messages = []
        if system_prompt:
            messages.append({"role": "system", "content": system_prompt})
        messages.append({"role": "user", "content": prompt})

        payload = {
            "model": model_name,
            "messages": messages,
            "temperature": 0.1,
        }

        provider_cfg: dict[str, Any] = {}
        if policy:
            if policy.zdr or not policy.allow_data_collection:
                provider_cfg["data_collection"] = "deny"
            if policy.zdr:
                provider_cfg["zdr"] = True
            if policy.openrouter_order:
                provider_cfg["order"] = policy.openrouter_order
            elif policy.openrouter_providers:
                provider_cfg["order"] = policy.openrouter_providers
            elif policy.allowed_providers:
                transports = {"openrouter", "opencode", "openai_compatible", "ollama", "lmstudio", "vllm", "groq", "cerebras"}
                upstream = [p for p in policy.allowed_providers if p.lower() not in transports]
                if upstream:
                    provider_cfg["order"] = upstream
            if policy.openrouter_ignore:
                provider_cfg["ignore"] = policy.openrouter_ignore
            if not policy.openrouter_allow_fallbacks:
                provider_cfg["allow_fallbacks"] = False
        if provider_cfg:
            payload["provider"] = provider_cfg
        # Native constrained decoding via OpenRouter (supports response_format for compliant models)
        schema = _extract_output_schema(prompt)
        if schema is not None:
            payload["response_format"] = {
                "type": "json_schema",
                "json_schema": {"name": "harness_fleet_output", "strict": True, "schema": schema},
            }

        def _do_post(_payload):
            # Ephemeral client under pytest for monkeypatch isolation; shared pooled client in prod
            if _should_use_ephemeral_client():
                with httpx.Client(timeout=timeout_sec, follow_redirects=True) as _cl:
                    return _cl.post(f"{self.base_url}/chat/completions", headers=headers, json=_payload)
            _cl = _shared_httpx_client(timeout=timeout_sec)
            return _cl.post(f"{self.base_url}/chat/completions", headers=headers, json=_payload, timeout=timeout_sec)

        try:
            resp = _do_post(payload)
            if resp.status_code == 400 and "response_format" in payload:
                from .base import SCHEMA_REJECTION_KEYWORDS

                error_body = resp.text.lower()
                if any(kw in error_body for kw in SCHEMA_REJECTION_KEYWORDS):
                    payload.pop("response_format", None)
                    resp = _do_post(payload)

            if resp.status_code == 429:
                retry_hdr = resp.headers.get("retry-after")
                try:
                    retry_sec = max(1.0, float(retry_hdr)) if retry_hdr else 10.0
                except (ValueError, TypeError):
                    retry_sec = 10.0
                receipt.error = f"Rate limited (429): {resp.text[:300]}"
                receipt.error_type = "rate_limit"
                receipt.retry_after = retry_sec
                receipt.duration_seconds = time.time() - started
                return False, None, receipt

            if resp.status_code in (500, 502, 503, 504):
                receipt.error = f"Transient HTTP {resp.status_code}: {resp.text[:300]}"
                receipt.error_type = "transient_http"
                receipt.retry_after = 5.0
                receipt.duration_seconds = time.time() - started
                return False, None, receipt

            if resp.status_code != 200:
                receipt.error = f"HTTP {resp.status_code}: {resp.text[:500]}"
                receipt.error_type = "inference_error"
                receipt.duration_seconds = time.time() - started
                return False, None, receipt

            data = resp.json()
            choices = data.get("choices", [])
            if not choices:
                receipt.error = "Empty choices in response"
                receipt.error_type = "inference_error"
                receipt.duration_seconds = time.time() - started
                return False, None, receipt

            text = choices[0].get("message", {}).get("content") or ""
            usage = data.get("usage", {})
            receipt.usage = usage

            reported_cost = usage.get("cost") if isinstance(usage, dict) else None
            if reported_cost is None:
                reported_cost = data.get("cost")
            if isinstance(reported_cost, (int, float)):
                receipt.cost = float(reported_cost)
                receipt.cost_status = "reported_zero" if reported_cost == 0 else "billed"

            receipt.status = "complete"
            receipt.duration_seconds = time.time() - started
            return True, text, receipt

        except httpx.TimeoutException:
            receipt.error = f"Request timed out after {timeout_sec}s"
            receipt.error_type = "timeout"
            receipt.duration_seconds = time.time() - started
            return False, None, receipt
        except Exception as e:
            receipt.error = str(e)
            receipt.error_type = "inference_error"
            receipt.duration_seconds = time.time() - started
            return False, None, receipt
