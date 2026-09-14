"""Muse CLI-harness adapter: spec + parser over the shared base.

CLI contract (muse-harness-handoff skill): ``muse exec --json --workspace
W --worktree off --prompt-file FILE``. Discovery is binary plus
``--version`` only, so no models command exists. History continuation is
interactive-only, so fresh-only execution holds by design: the engine
fresh-runs every prompt.
"""
from __future__ import annotations

from pathlib import Path

from .harness import (
    CLIHarnessProvider,
    HarnessSpec,
    ProviderReceipt,
    parse_json_object_stdout,
    run_error_receipt,
)

MUSE_SPEC = HarnessSpec(
    name="muse",
    binary="muse",
    prompt_delivery="file_flag",
    prompt_file_flag="--prompt-file",
    parser="json_object",
    call_workdir=True,
    discovery_argv=None,
    model_from_route=False,
)


class MuseProvider(CLIHarnessProvider):
    def __init__(self, runner=None):
        super().__init__(MUSE_SPEC, runner=runner)

    def build_argv(
        self,
        *,
        model: str,
        prompt: str,
        prompt_file: str | None,
        workspace: Path | None = None,
        workdir: Path | None = None,
    ) -> list[str]:
        # No --model in the documented headless recipe: the CLI default serves.
        if prompt_file is None:
            raise RuntimeError("muse prompt staging failed")
        return [
            "exec",
            "--json",
            "--workspace", str(workspace or Path.cwd()),
            "--worktree", "off",
            "--prompt-file", prompt_file,
        ]

    def parse_output(
        self,
        *,
        code: int,
        stdout: str,
        stderr: str,
        receipt: ProviderReceipt,
        started: float,
        workdir: Path | None = None,
    ) -> tuple[bool, str | None, ProviderReceipt]:
        if code != 0:
            return run_error_receipt(code, stdout, stderr, receipt=receipt)
        return parse_json_object_stdout(stdout, receipt=receipt)
