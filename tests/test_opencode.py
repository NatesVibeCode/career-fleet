import json
from types import SimpleNamespace

from harness_fleet.providers.opencode import LocalOpenCodeCLI, OpenCodeProvider


class RunnerStub:
    def run(self, task_config, args, timeout_sec):
        events = [
            {"type": "text", "part": {"text": "{}"}},
            {"type": "step_finish", "part": {"tokens": {"total_tokens": 1}}},
        ]
        return 0, "\n".join(json.dumps(event) for event in events), ""


def test_missing_cost_is_unknown_not_zero():
    ok, _, receipt = OpenCodeProvider(runner=RunnerStub()).run_prompt("opencode/name-free", "prompt")
    assert ok is True
    assert receipt.cost is None
    assert receipt.cost_status == "unknown"


def test_discovered_opencode_model_keeps_native_provider_prefix():
    class CheckingRunner(RunnerStub):
        def run(self, task_config, args, timeout_sec):
            assert task_config["model"] == "opencode/name-free"
            assert args[args.index("--model") + 1] == "opencode/name-free"
            return super().run(task_config, args, timeout_sec)

    assert OpenCodeProvider(runner=CheckingRunner()).run_prompt("opencode/name-free", "prompt")[0]


def test_local_runner_invokes_normal_opencode_cli(monkeypatch):
    captured = {}

    def fake_run(command, cwd, capture_output, text, timeout):
        captured["command"] = command
        captured["config"] = json.loads(__import__("pathlib").Path(cwd, "opencode.json").read_text())
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    monkeypatch.setattr("harness_fleet.providers.opencode.subprocess.run", fake_run)
    LocalOpenCodeCLI().run({"permission": {"*": "deny"}, "mcp": {}}, ["run", "prompt"], 10)

    assert captured["command"] == ["opencode", "run", "prompt"]
    assert captured["config"] == {"permission": {"*": "deny"}, "mcp": {}}
