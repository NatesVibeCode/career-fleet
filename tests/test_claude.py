import json

from harness_fleet.providers.claude import ClaudeProvider


class RunnerStub:
    def __init__(self, stdout="", code=0, stderr=""):
        self.seen = {}
        self.stdout = stdout
        self.code = code
        self.stderr = stderr

    def run(self, *, argv, stdin_text, timeout_sec):
        self.seen = {"argv": argv, "stdin_text": stdin_text, "timeout_sec": timeout_sec}
        return self.code, self.stdout, self.stderr


def _provider(runner):
    provider = ClaudeProvider(runner=runner)
    provider._preflight = lambda: None  # type: ignore[method-assign]
    return provider


def test_argv_and_stdin_delivery():
    runner = RunnerStub(stdout=json.dumps({"result": '{"items": []}', "usage": {"total_tokens": 3}}))
    ok, text, receipt = _provider(runner).run_prompt("claude/some-model", "do the thing")
    assert ok is True
    assert text == '{"items": []}'
    argv = runner.seen["argv"]
    assert isinstance(argv, list) and all(isinstance(part, str) for part in argv)
    assert argv[:3] == ["claude", "-p", "--output-format"]
    assert argv[3] == "json"
    assert runner.seen["stdin_text"] == "do the thing"
    assert all("do the thing" not in part for part in argv)


def test_missing_cost_is_unknown_not_zero():
    runner = RunnerStub(stdout=json.dumps({"result": "text", "usage": {}}))
    ok, _, receipt = _provider(runner).run_prompt("claude/some-model", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"


def test_error_object_fails_closed():
    runner = RunnerStub(stdout=json.dumps({"error": "boom"}))
    ok, text, receipt = _provider(runner).run_prompt("claude/some-model", "prompt")
    assert ok is False and text is None
    assert receipt.error_type == "inference_error"


def test_nonzero_exit_is_error_receipt():
    runner = RunnerStub(stdout="", code=1, stderr="rate limit 429")
    ok, _, receipt = _provider(runner).run_prompt("claude/some-model", "prompt")
    assert ok is False
    assert receipt.error_type == "rate_limit"
    assert receipt.retry_after == 10.0


def test_timeout_receipt():
    import subprocess

    class TimeoutRunner:
        def run(self, *, argv, stdin_text, timeout_sec):
            raise subprocess.TimeoutExpired(cmd=argv, timeout=timeout_sec)

    ok, text, receipt = _provider(TimeoutRunner()).run_prompt("claude/some-model", "prompt")  # type: ignore[arg-type]
    assert ok is False and text is None
    assert receipt.error_type == "timeout"
