import json
from pathlib import Path

from harness_fleet.providers.grok import GrokProvider


class RunnerStub:
    def __init__(self, stdout="", code=0, stderr=""):
        self.seen = {}
        self.stdout = stdout
        self.code = code
        self.stderr = stderr

    def run(self, *, argv, stdin_text, timeout_sec):
        file_text = None
        if "--prompt-file" in argv:
            file_text = Path(argv[argv.index("--prompt-file") + 1]).read_text(encoding="utf-8")
        self.seen = {"argv": argv, "stdin_text": stdin_text, "timeout_sec": timeout_sec, "file_text": file_text}
        return self.code, self.stdout, self.stderr


def _provider(runner):
    provider = GrokProvider(runner=runner)
    provider._preflight = lambda: None  # type: ignore[method-assign]
    return provider


def test_argv_uses_prompt_file_with_exact_bytes():
    runner = RunnerStub(stdout=json.dumps({"result": '{"items": []}'}))
    ok, text, _ = _provider(runner).run_prompt("grok/some-model", "do the thing")
    assert ok is True
    assert text == '{"items": []}'
    argv = runner.seen["argv"]
    assert isinstance(argv, list) and all(isinstance(part, str) for part in argv)
    assert argv[0] == "grok"
    assert argv[argv.index("--cwd") + 1]
    assert argv[argv.index("--output-format") + 1] == "json"
    assert runner.seen["file_text"] == "do the thing"
    assert runner.seen["stdin_text"] is None


def test_missing_cost_is_unknown_not_zero():
    runner = RunnerStub(stdout=json.dumps({"result": "text"}))
    ok, _, receipt = _provider(runner).run_prompt("grok/some-model", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"


def test_error_object_fails_closed():
    runner = RunnerStub(stdout=json.dumps({"error": "boom"}))
    ok, text, receipt = _provider(runner).run_prompt("grok/some-model", "prompt")
    assert ok is False and text is None
    assert receipt.error_type == "inference_error"


def test_nonzero_exit_is_error_receipt():
    runner = RunnerStub(stdout="", code=1, stderr="rate limit exceeded")
    ok, _, receipt = _provider(runner).run_prompt("grok/some-model", "prompt")
    assert ok is False
    assert receipt.error_type == "rate_limit"


def test_timeout_receipt():
    import subprocess

    class TimeoutRunner:
        def run(self, *, argv, stdin_text, timeout_sec):
            raise subprocess.TimeoutExpired(cmd=argv, timeout=timeout_sec)

    ok, text, receipt = _provider(TimeoutRunner()).run_prompt("grok/some-model", "prompt")  # type: ignore[arg-type]
    assert ok is False and text is None
    assert receipt.error_type == "timeout"
