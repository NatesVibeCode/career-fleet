import json
from pathlib import Path

from harness_fleet.providers.muse import MuseProvider


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
    provider = MuseProvider(runner=runner)
    provider._preflight = lambda: None  # type: ignore[method-assign]
    return provider


def test_argv_uses_prompt_file_with_exact_bytes():
    runner = RunnerStub(stdout=json.dumps({"result": '{"items": []}'}))
    ok, text, _ = _provider(runner).run_prompt("muse/some-model", "do the thing")
    assert ok is True
    assert text == '{"items": []}'
    argv = runner.seen["argv"]
    assert isinstance(argv, list) and all(isinstance(part, str) for part in argv)
    assert argv[:2] == ["muse", "exec"]
    assert "--json" in argv
    assert argv[argv.index("--workspace") + 1]
    assert argv[argv.index("--worktree") + 1] == "off"
    assert runner.seen["file_text"] == "do the thing"
    assert runner.seen["stdin_text"] is None


def test_missing_cost_is_unknown_not_zero():
    runner = RunnerStub(stdout=json.dumps({"result": "text"}))
    ok, _, receipt = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"


def test_error_object_fails_closed():
    runner = RunnerStub(stdout=json.dumps({"error": "boom"}))
    ok, text, receipt = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is False and text is None
    assert receipt.error_type == "inference_error"


def test_nonzero_exit_is_error_receipt():
    runner = RunnerStub(stdout="", code=1, stderr="nope")
    ok, _, receipt = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is False
    assert receipt.error_type == "inference_error"


def test_timeout_receipt():
    import subprocess

    class TimeoutRunner:
        def run(self, *, argv, stdin_text, timeout_sec):
            raise subprocess.TimeoutExpired(cmd=argv, timeout=timeout_sec)

    ok, text, receipt = _provider(TimeoutRunner()).run_prompt("muse/some-model", "prompt")  # type: ignore[arg-type]
    assert ok is False and text is None
    assert receipt.error_type == "timeout"


def _muse_stream(terminal, *, text="PING", reason=None, deltas=()):
    """NDJSON session events in the shape ``muse exec --json`` really emits."""
    lines = [
        json.dumps({
            "schema_version": 1, "record_type": "status",
            "payload_type": "run.output.delta",
            "payload": {"kind": "run_output_delta", "text": chunk},
        })
        for chunk in deltas
    ]
    lines.append(json.dumps({
        "schema_version": 1, "record_type": "event",
        "payload_type": f"run.terminal.{terminal}",
        "payload": {"kind": "run_terminal", "terminal": terminal, "text": text, "reason": reason},
    }))
    return "\n".join(lines) + "\n"


def test_ndjson_stream_terminal_text_is_read():
    """The real headless transcript is NDJSON; a single-object parse never worked."""
    runner = RunnerStub(stdout=_muse_stream("completed", text="PING"))
    ok, text, receipt = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is True
    assert text == "PING"
    assert receipt.status == "complete"


def test_ndjson_deltas_are_used_when_no_terminal_record_exists():
    runner = RunnerStub(stdout=json.dumps({
        "payload_type": "run.output.delta",
        "payload": {"kind": "run_output_delta", "text": "PIN"},
    }) + "\n" + json.dumps({
        "payload_type": "run.output.delta",
        "payload": {"kind": "run_output_delta", "text": "G"},
    }) + "\n")
    ok, text, _ = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is True
    assert text == "PING"


def test_ndjson_failed_terminal_fails_closed_with_reason():
    runner = RunnerStub(stdout=_muse_stream("failed", text="", reason="model unavailable"))
    ok, text, receipt = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is False and text is None
    assert "model unavailable" in (receipt.error or "")


def test_ndjson_failed_terminal_without_text_never_invents_an_answer():
    runner = RunnerStub(stdout=_muse_stream("cancelled", text="", deltas=("partial",)))
    ok, text, _ = _provider(runner).run_prompt("muse/some-model", "prompt")
    assert ok is False and text is None
