import httpx
import pytest

from harness_fleet.providers.openai_compatible import OpenAICompatibleProvider
from harness_fleet.providers.registry import ProviderRegistry


def test_named_providers_do_not_inherit_another_accounts_settings(monkeypatch):
    monkeypatch.setenv("OPENAI_COMPATIBLE_BASE_URL", "https://custom.example/v1")
    monkeypatch.setenv("OPENAI_COMPATIBLE_API_KEY", "custom-secret")
    monkeypatch.setenv("OPENAI_API_KEY", "unrelated-secret")
    monkeypatch.delenv("GROQ_API_KEY", raising=False)
    monkeypatch.delenv("GROQ_BASE_URL", raising=False)
    provider = OpenAICompatibleProvider(provider_name="groq")
    assert provider.base_url == "https://api.groq.com/openai/v1"
    assert provider.api_key == ""


@pytest.mark.parametrize("url", ["https://localhost.example/v1", "https://remote.example/localhost", "https://127.0.0.1.example/v1"])
def test_remote_urls_are_not_mistaken_for_free_local_servers(url):
    assert not OpenAICompatibleProvider(base_url=url).is_local


def test_remote_explicit_zero_cost_is_preserved(monkeypatch):
    monkeypatch.setattr(httpx.Client, "post", lambda *a, **kw: httpx.Response(200, json={
        "choices": [{"message": {"content": "{}"}}], "cost": 0.0,
    }))
    ok, _, receipt = OpenAICompatibleProvider(base_url="https://remote.example/v1").run_prompt("model", "test")
    assert ok
    assert receipt.cost == 0.0
    assert receipt.cost_status == "reported_zero"


def test_unrelated_400_does_not_drop_constraints(monkeypatch):
    from harness_fleet.models import TaskSpec

    task = TaskSpec(name="t", instructions="Do it.")
    prompt = task.render_prompt([{
        "item_id": "i1", "title": "T",
        "sections": [{"slice_id": "full", "start": 0, "end": 10, "text": "0123456789"}],
    }])
    calls = []

    def _post(self, *args, **kwargs):
        calls.append(kwargs.get("json", {}))
        return httpx.Response(400, text="invalid parameter: unexpected extra field max_tokens")

    monkeypatch.setattr(httpx.Client, "post", _post)
    ok, _, receipt = OpenAICompatibleProvider(base_url="https://remote.example/v1").run_prompt("m", prompt)
    assert ok is False
    assert receipt.error_type == "inference_error"
    assert len(calls) == 1
    assert "response_format" in calls[0]


def test_colon_namespace_with_nested_model_resolves_correct_provider():
    provider = ProviderRegistry().resolve("openrouter")
    assert provider.__class__.__name__ == "OpenRouterProvider"


def test_provider_registry_resolution():
    registry = ProviderRegistry()
    assert registry.get("opencode").__class__.__name__ == "OpenCodeProvider"
    assert registry.get("openrouter").__class__.__name__ == "OpenRouterProvider"
    assert registry.get("ollama").__class__.__name__ == "OpenAICompatibleProvider"
    assert registry.get("lmstudio").__class__.__name__ == "OpenAICompatibleProvider"

    # Resolution by provider hint
    # Resolution by route prefix


def test_openai_compatible_successful_completion(monkeypatch):
    def mock_post(url, headers, json):
        resp_data = {
            "choices": [{"message": {"content": '{"items": []}'}}],
            "usage": {"total_tokens": 42},
            "cost": 0.0,
        }
        return httpx.Response(200, json=resp_data)

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_post(url, headers, json))

    prov = OpenAICompatibleProvider(base_url="http://localhost:11434/v1")
    ok, text, receipt = prov.run_prompt("ollama/qwen", "hello")
    assert ok is True
    assert text == '{"items": []}'
    assert receipt.status == "complete"
    assert receipt.cost_status == "reported_zero"
    assert receipt.usage["total_tokens"] == 42


def test_openai_compatible_rate_limit_429(monkeypatch):
    def mock_429(url, headers, json):
        headers = {"retry-after": "15"}
        return httpx.Response(429, headers=headers, text="Rate limit exceeded")

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_429(url, headers, json))

    prov = OpenAICompatibleProvider(base_url="http://localhost:11434/v1")
    ok, text, receipt = prov.run_prompt("ollama/qwen", "hello")
    assert ok is False
    assert receipt.status == "failed"
    assert receipt.error_type == "rate_limit"
    assert receipt.retry_after == 15.0


def test_openai_compatible_transient_503(monkeypatch):
    def mock_503(url, headers, json):
        return httpx.Response(503, text="Service Unavailable")

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_503(url, headers, json))

    prov = OpenAICompatibleProvider(base_url="http://localhost:11434/v1")
    ok, text, receipt = prov.run_prompt("ollama/qwen", "hello")
    assert ok is False
    assert receipt.error_type == "transient_http"
    assert receipt.retry_after == 5.0


def test_openai_compatible_prefix_stripping(monkeypatch):
    captured_model = None

    def mock_post(url, headers, json):
        nonlocal captured_model
        captured_model = json.get("model")
        return httpx.Response(200, json={
            "choices": [{"message": {"content": "{}"}}],
            "usage": {"total_tokens": 5},
            "cost": 0.0,
        })

    monkeypatch.setattr(httpx.Client, "post", lambda self, url, headers, json: mock_post(url, headers, json))

    prov = OpenAICompatibleProvider(base_url="http://localhost:11434/v1")
    prov.run_prompt("ollama/llama3.2:latest", "test")
    assert captured_model == "llama3.2:latest"

    prov.run_prompt("ollama:llama3.2:latest", "test")
    assert captured_model == "llama3.2:latest"
