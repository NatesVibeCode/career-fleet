import httpx

from harness_fleet.models import RoutePolicy
from harness_fleet.providers.openrouter import OpenRouterProvider


def test_openrouter_injects_zdr_and_privacy_controls(monkeypatch):
    captured_payload = {}

    def mock_post(url, headers, json):
        nonlocal captured_payload
        captured_payload = json
        return httpx.Response(200, json={
            "choices": [{"message": {"content": "ok"}}],
            "cost": 0.0,
        })

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_post(url, headers, json))

    prov = OpenRouterProvider(api_key="test-key")
    policy = RoutePolicy(
        zdr=True,
        allow_data_collection=False,
        allowed_providers=["deepinfra", "together"],
    )

    ok, text, receipt = prov.run_prompt("openrouter/meta/llama-3:free", "hello", policy=policy)
    assert ok is True
    assert "provider" in captured_payload
    assert captured_payload["provider"]["zdr"] is True
    assert captured_payload["provider"]["data_collection"] == "deny"
    assert captured_payload["provider"]["order"] == ["deepinfra", "together"]


def test_openrouter_rate_limit_populates_retry_after(monkeypatch):
    def mock_429(url, headers, json):
        return httpx.Response(429, headers={"retry-after": "8"}, text="Too many requests")

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_429(url, headers, json))

    prov = OpenRouterProvider(api_key="test-key")
    ok, text, receipt = prov.run_prompt("openrouter/meta/llama-3:free", "hello")
    assert ok is False
    assert receipt.error_type == "rate_limit"
    assert receipt.retry_after == 8.0
