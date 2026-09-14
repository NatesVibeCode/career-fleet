import json

from harness_fleet.providers.codex import CodexProvider


class RunnerStub:
    def __init__(self, stdout="", code=0, stderr=""):
        self.seen = {}
        self.stdout = stdout
        self.code = code
        self.stderr = stderr

    def run(self, *, argv, stdin_text, timeout_sec):
        self.seen = {"argv": argv, "stdin_text": stdin_text, "timeout_sec": timeout_sec}
        return self.code, self.stdout, self.stderr


EVENTS = "\n".join(json.dumps(event) for event in [
    {"type": "message", "message": '{"items": []}'},
    {"type": "usage", "usage": {"total_tokens": 5}},
])


def _provider(runner):
    provider = CodexProvider(runner=runner)
    provider._preflight = lambda: None  # type: ignore[method-assign]
    return provider


def test_argv_and_stdin_delivery():
    runner = RunnerStub(stdout=EVENTS)
    ok, text, _ = _provider(runner).run_prompt("codex/some-model", "do the thing")
    assert ok is True
    assert text == '{"items": []}'
    argv = runner.seen["argv"]
    assert isinstance(argv, list) and all(isinstance(part, str) for part in argv)
    assert argv[0] == "codex"
    assert argv[1] == "exec"
    assert argv[argv.index("-C") + 1]
    assert argv[argv.index("--json") + 1] == "-o"
    assert argv[argv.index("-o") + 1].endswith("final.md")
    assert argv[-1] == "-"
    assert runner.seen["stdin_text"] == "do the thing"


def test_missing_cost_is_unknown_not_zero():
    runner = RunnerStub(stdout=EVENTS)
    ok, _, receipt = _provider(runner).run_prompt("codex/some-model", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"


def test_error_event_fails_closed():
    runner = RunnerStub(stdout=json.dumps({"type": "error", "error": "boom"}))
    ok, text, receipt = _provider(runner).run_prompt("codex/some-model", "prompt")
    assert ok is False and text is None
    assert receipt.error_type == "inference_error"


def test_nonzero_exit_is_error_receipt():
    runner = RunnerStub(stdout="", code=1, stderr="429 slow down")
    ok, _, receipt = _provider(runner).run_prompt("codex/some-model", "prompt")
    assert ok is False
    assert receipt.error_type == "rate_limit"


def test_final_file_is_fallback_when_stdout_has_no_text(tmp_path):
    import json
    (tmp_path / "final.md").write_text(json.dumps({"text": "final-file-text"}), encoding="utf-8")
    from harness_fleet.providers.codex import parse_codex_jsonl
    from harness_fleet.providers.harness import ProviderReceipt

    receipt = ProviderReceipt(
        id="r-test", provider="codex", requested_route="codex/test",
        status="complete",
    )
    ok, text, receipt = parse_codex_jsonl("", "", 0, receipt=receipt, workdir=tmp_path)
    # The file content is a JSON envelope; conservative parser passes it
    # through clean_llm_json (returns the JSON string) — not garbage.
    assert ok is True
    assert text is not None and "final-file-text" in text
    assert receipt.status == "complete"


def test_final_file_ignored_on_nonzero_exit(tmp_path):
    (tmp_path / "final.md").write_text("stale", encoding="utf-8")
    from harness_fleet.providers.codex import parse_codex_jsonl
    from harness_fleet.providers.harness import ProviderReceipt

    receipt = ProviderReceipt(
        id="r-test", provider="codex", requested_route="codex/test",
        status="failed", error=None, error_type=None,
    )
    ok, text, _ = parse_codex_jsonl("", "bad", 1, receipt=receipt, workdir=tmp_path)
    assert ok is False and text is None


def test_timeout_receipt():
    import subprocess

    class TimeoutRunner:
        def run(self, *, argv, stdin_text, timeout_sec):
            raise subprocess.TimeoutExpired(cmd=argv, timeout=timeout_sec)

    ok, text, receipt = _provider(TimeoutRunner()).run_prompt("codex/some-model", "prompt")  # type: ignore[arg-type]
    assert ok is False and text is None
    assert receipt.error_type == "timeout"
