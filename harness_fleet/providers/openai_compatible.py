"""Generic OpenAI-compatible API provider supporting Ollama, LM Studio, vLLM, Groq, etc."""
from __future__ import annotations

import os
import threading
import time
import uuid
from typing import Literal
from urllib.parse import urlsplit

import httpx

from ..models import ProviderReceipt, RoutePolicy
from .base import BaseProvider

_shared_client: httpx.Client | None = None
_client_lock = threading.Lock()

def _shared_httpx_client(timeout: int = 120) -> httpx.Client:
    global _shared_client
    if os.environ.get("PYTEST_CURRENT_TEST"):
        return httpx.Client(timeout=timeout, follow_redirects=True)
    with _client_lock:
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

def _should_use_ephemeral() -> bool:
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


def _parse_retry_after(header_val: str | None) -> float:
    if not header_val:
        return 10.0
    try:
        return max(1.0, float(header_val))
    except (ValueError, TypeError):
        return 10.0


class OpenAICompatibleProvider(BaseProvider):
    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        provider_name: str = "openai_compatible",
        is_free: bool | None = None,
    ):
        # Service-specific environment defaults
        env_prefix = provider_name.upper()
        configured_url = (
            base_url
            or os.environ.get(f"{env_prefix}_BASE_URL")
        )
        if not configured_url:
            if provider_name == "groq":
                configured_url = "https://api.groq.com/openai/v1"
            elif provider_name == "cerebras":
                configured_url = "https://api.cerebras.ai/v1"
            elif provider_name == "lmstudio":
                configured_url = "http://localhost:1234/v1"
            elif provider_name == "vllm":
                configured_url = "http://localhost:8000/v1"
            elif provider_name == "ollama":
                configured_url = "http://localhost:11434/v1"
            else:
                configured_url = "http://localhost:11434/v1"

        self.base_url = configured_url.rstrip("/")
        self.api_key = (
            api_key if api_key is not None else os.environ.get(f"{env_prefix}_API_KEY", "")
        )
        self.provider_name = provider_name
        self.is_local = (
            is_free
            if is_free is not None
            else urlsplit(self.base_url).hostname in {"localhost", "127.0.0.1", "0.0.0.0", "::1"}
        )

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

        # Strip provider prefix if present (e.g., ollama/llama3 -> llama3, openai/gpt-4o -> gpt-4o)
        model_name = route_id
        prefixes = (
            "openai_compatible/", "ollama/", "lmstudio/", "vllm/", "groq/", "cerebras/", "openai/",
            "openai_compatible:", "ollama:", "lmstudio:", "vllm:", "groq:", "cerebras:", "openai:",
        )
        for prefix in prefixes:
            if model_name.startswith(prefix):
                model_name = model_name[len(prefix):]
                break

        # Cost-safety: only assert reported_zero if verified local/free; otherwise unknown
        default_cost = 0.0 if self.is_local else None
        default_cost_status: Literal["reported_zero", "billed", "unknown"] = (
            "reported_zero" if self.is_local else "unknown"
        )

        receipt: ProviderReceipt = ProviderReceipt(
            id=rid,
            session_id=session_id,
            provider=self.provider_name,
            requested_route=route_id,
            status="failed",
            cost=default_cost,
            cost_status=default_cost_status,
            usage=None,
            error=None,
            error_type=None,
            retry_after=None,
            duration_seconds=None,
        )

        headers = {"Content-Type": "application/json"}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"

        messages = []
        if system_prompt:
            messages.append({"role": "system", "content": system_prompt})
        messages.append({"role": "user", "content": prompt})

        payload = {
            "model": model_name,
            "messages": messages,
            "temperature": 0.1,
        }
        # Native constrained decoding: pass JSON Schema when available (vLLM, Ollama, Groq, Cerebras support it)
        schema = _extract_output_schema(prompt)
        if schema is not None:
            # Prefer strict json_schema; fallback to json_object is handled per-provider
            payload["response_format"] = {
                "type": "json_schema",
                "json_schema": {"name": "harness_fleet_output", "strict": True, "schema": schema},
            }

        def _do_post(_payload):
            if _should_use_ephemeral():
                with httpx.Client(timeout=timeout_sec, follow_redirects=True) as _cl:
                    return _cl.post(f"{self.base_url}/chat/completions", headers=headers, json=_payload)
            _cl = _shared_httpx_client(timeout=timeout_sec)
            return _cl.post(f"{self.base_url}/chat/completions", headers=headers, json=_payload, timeout=timeout_sec)

        try:
            resp = _do_post(payload)
            # If provider rejects json_schema, retry once with json_object or raw text
            if resp.status_code == 400 and "response_format" in payload:
                from .base import SCHEMA_REJECTION_KEYWORDS

                error_body = resp.text.lower()
                if any(kw in error_body for kw in SCHEMA_REJECTION_KEYWORDS):
                    payload.pop("response_format", None)
                    if self.provider_name in ("ollama", "lmstudio", "vllm", "groq", "cerebras", "openai_compatible"):
                        payload["response_format"] = {"type": "json_object"}
                        resp = _do_post(payload)
                        if resp.status_code == 400:
                            payload.pop("response_format", None)
                            resp = _do_post(payload)
                    else:
                        resp = _do_post(payload)

            if resp.status_code == 429:
                retry_sec = _parse_retry_after(resp.headers.get("retry-after"))
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

            reported_cost = data.get("cost")
            if reported_cost is None and isinstance(usage, dict):
                reported_cost = usage.get("cost")
            if isinstance(reported_cost, (int, float)):
                receipt.cost = float(reported_cost)
                receipt.cost_status = "reported_zero" if reported_cost == 0 else "billed"
            elif self.is_local:
                receipt.cost = 0.0
                receipt.cost_status = "reported_zero"
            else:
                receipt.cost = None
                receipt.cost_status = "unknown"

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
