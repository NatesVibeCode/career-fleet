from harness_fleet.providers.openrouter import OpenRouterProvider


class FakeResponse:
    status_code = 200
    text = ""

    def json(self):
        return {
            "choices": [{"message": {"content": "{}"}}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1},
        }


class FakeClient:
    def __init__(self, *args, **kwargs):
        pass

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return None

    def post(self, *args, **kwargs):
        return FakeResponse()


def _schema_prompt():
    from harness_fleet.models import TaskSpec

    task = TaskSpec(name="t", instructions="Do it.")
    return task.render_prompt([{
        "item_id": "i1", "title": "T",
        "sections": [{"slice_id": "full", "start": 0, "end": 10, "text": "0123456789"}],
    }])


class _ScriptedClient:
    """Replays scripted (status, body) responses while counting posts."""
    responses = ()
    posts = 0

    def __init__(self, *args, **kwargs):
        pass

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return None

    def post(self, *args, **kwargs):
        type(self).posts += 1
        status, body = self.responses[min(type(self).posts - 1, len(self.responses) - 1)]

        class _Resp:
            status_code = status
            text = body
            headers = {}

            def json(self):
                return {"choices": [{"message": {"content": "{}"}}], "usage": {}}

        return _Resp()


def test_schema_rejection_retries_without_response_format(monkeypatch):
    _ScriptedClient.responses = (
        (400, "error: response_format json_schema not supported by this model"),
        (200, "ok"),
    )
    _ScriptedClient.posts = 0
    monkeypatch.setattr("harness_fleet.providers.openrouter.httpx.Client", _ScriptedClient)
    ok, _, _ = OpenRouterProvider(api_key="key").run_prompt("openrouter/m", _schema_prompt())
    assert ok is True
    assert _ScriptedClient.posts == 2


def test_unrelated_400_does_not_retry(monkeypatch):
    _ScriptedClient.responses = ((400, "invalid parameter: max_tokens out of range"),)
    _ScriptedClient.posts = 0
    monkeypatch.setattr("harness_fleet.providers.openrouter.httpx.Client", _ScriptedClient)
    ok, _, receipt = OpenRouterProvider(api_key="key").run_prompt("openrouter/m", _schema_prompt())
    assert ok is False
    assert receipt.error_type == "inference_error"
    assert _ScriptedClient.posts == 1


def test_free_suffix_does_not_manufacture_zero_cost(monkeypatch):
    monkeypatch.setattr("harness_fleet.providers.openrouter.httpx.Client", FakeClient)
    ok, _, receipt = OpenRouterProvider(api_key="key").run_prompt("openrouter/example:free", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"
