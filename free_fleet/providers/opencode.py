from __future__ import annotations

import json
import subprocess
import tempfile
import time
import uuid
from pathlib import Path
from typing import Any, Optional, Protocol, Tuple

from .base import BaseProvider


class OpenCodeRunner(Protocol):
    def run(self, task_config: dict, args: list[str], timeout_sec: int) -> tuple[int, str, str]: ...


class LocalOpenCodeCLI:
    """Invoke OpenCode normally while applying a task-local no-tools config."""

    def run(self, task_config: dict, args: list[str], timeout_sec: int) -> tuple[int, str, str]:
        with tempfile.TemporaryDirectory(prefix="free-fleet-opencode-") as temp_dir:
            Path(temp_dir, "opencode.json").write_text(json.dumps(task_config, indent=2))
            completed = subprocess.run(
                ["opencode", *args],
                cwd=temp_dir,
                capture_output=True,
                text=True,
                timeout=timeout_sec,
            )
        return completed.returncode, completed.stdout, completed.stderr

class OpenCodeProvider(BaseProvider):
    def __init__(self, runner: OpenCodeRunner | None = None):
        self.runner = runner or LocalOpenCodeCLI()

    def run_prompt(
        self,
        route_id: str,
        prompt: str,
        system_prompt: Optional[str] = None,
        timeout_sec: int = 120,
        session_id: Optional[str] = None,
        policy: Optional[Any] = None,
    ) -> Tuple[bool, Optional[str], dict]:
        started = time.time()
        rid = uuid.uuid4().hex
        receipt = {
            "id": rid,
            "session_id": session_id,
            "provider": "opencode",
            "requested_route": route_id,
            "status": "failed",
            "cost": None,
            "cost_status": "unknown",
            "usage": None,
            "error": None,
            "error_type": None,
            "retry_after": None,
            "duration_seconds": None,
        }

        full_prompt = (system_prompt + "\n\n" if system_prompt else "") + prompt
        
        # Strip provider namespace prefix if present (e.g. opencode/anthropic/claude-3-5-sonnet -> anthropic/claude-3-5-sonnet)
        actual_model = route_id
        if actual_model.startswith("opencode/") and "/" in actual_model[len("opencode/"):]:
            actual_model = actual_model[len("opencode/"):]
        elif actual_model.startswith("opencode:"):
            actual_model = actual_model[len("opencode:"):]

        # Task-local OpenCode configuration: normal CLI auth, no model tools or MCP.
        task_config = {
            "$schema": "https://opencode.ai/config.json",
            "model": actual_model,
            "permission": {"*": "deny"},
            "mcp": {},
            "share": "disabled"
        }

        args = ["run", "--format", "json", "--model", actual_model]
        args.append(full_prompt)

        try:
            code, stdout, stderr = self.runner.run(
                task_config=task_config,
                args=args,
                timeout_sec=timeout_sec
            )

            texts = []
            finished = False
            costs = []
            last_err = None

            for line in stdout.splitlines():
                line = line.strip()
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue

                event_type = event.get("type")
                part = event.get("part", {})

                if event_type == "text":
                    texts.append(part.get("text", ""))
                elif event_type == "step_finish":
                    finished = True
                    c = part.get("cost")
                    if c is not None:
                        costs.append(c)
                    receipt["usage"] = part.get("tokens")
                elif event_type == "error":
                    last_err = str(event.get("error", ""))[:500]

            if code != 0 or not finished or not texts:
                err_msg = last_err or (stderr or stdout)[-500:] or f"Exit code {code}"
                receipt["error"] = err_msg
                if "429" in err_msg.lower() or "rate limit" in err_msg.lower():
                    receipt["error_type"] = "rate_limit"
                    receipt["retry_after"] = 10.0
                else:
                    receipt["error_type"] = "inference_error"
                receipt["duration_seconds"] = time.time() - started
                return False, None, receipt

            if costs and all(isinstance(c, (int, float)) for c in costs):
                total_cost = float(sum(costs))
                receipt["cost"] = total_cost
                receipt["cost_status"] = "reported_zero" if total_cost == 0 else "billed"
            receipt["status"] = "complete"
            receipt["duration_seconds"] = time.time() - started
            
            return True, "\n".join(texts), receipt

        except Exception as e:
            receipt["error"] = str(e)
            if isinstance(e, subprocess.TimeoutExpired):
                receipt["error_type"] = "timeout"
            else:
                receipt["error_type"] = "inference_error"
            receipt["duration_seconds"] = time.time() - started
            return False, None, receipt
