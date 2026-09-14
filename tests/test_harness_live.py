"""Opt-in live harness smoke: one PING through each real binary, else skip.

A binary present but not operational (no auth, no model) skips instead of
failing: stub tests prove parsing, this proves end-to-end wiring where the
environment allows it. The H1 commit message records the ran/skipped matrix.

Only *environmental* failures skip. A binary that is installed and
authenticated but answers with a bad receipt is a wiring regression, so it
fails here rather than hiding as another skip — otherwise this file can never
report the breakage it exists to catch.
"""
import shutil

import pytest

from harness_fleet.providers.antigravity import AntigravityProvider
from harness_fleet.providers.claude import ClaudeProvider
from harness_fleet.providers.codex import CodexProvider
from harness_fleet.providers.cursor import CursorProvider
from harness_fleet.providers.grok import GrokProvider
from harness_fleet.providers.muse import MuseProvider
from harness_fleet.providers.opencode import OpenCodeProvider

#: Failure kinds explained by the machine, not by our code: missing credentials,
#: a provider-side outage, or a slow model. Anything else is ours to fix.
ENVIRONMENTAL_ERROR_TYPES = frozenset(
    {"auth_error", "rate_limit", "timeout", "transient_http"}
)

CASES = [
    ("opencode", "opencode", OpenCodeProvider, "opencode/harness-fleet-ping"),
    ("claude", "claude", ClaudeProvider, "claude/harness-fleet-ping"),
    ("codex", "codex", CodexProvider, "codex/harness-fleet-ping"),
    ("cursor", "cursor-agent", CursorProvider, "cursor/harness-fleet-ping"),
    ("grok", "grok", GrokProvider, "grok/harness-fleet-ping"),
    ("muse", "muse", MuseProvider, "muse/harness-fleet-ping"),
    ("antigravity", "agy", AntigravityProvider, "antigravity/harness-fleet-ping"),
]


@pytest.mark.parametrize("name,binary,provider_cls,route_id", CASES)
def test_live_ping(name, binary, provider_cls, route_id):
    if shutil.which(binary) is None:
        pytest.skip(f"{binary} not installed")
    ok, text, receipt = provider_cls().run_prompt(
        route_id, "Reply with exactly: PING", timeout_sec=120
    )
    assert receipt.provider == name
    if not ok:
        detail = f"{name} not operational here: {(receipt.error or '')[:200]}"
        if receipt.error_type in ENVIRONMENTAL_ERROR_TYPES:
            pytest.skip(detail)
        pytest.fail(
            f"{name} is installed but failed with error_type={receipt.error_type!r}: {detail}"
        )
    assert text is not None and "PING" in text
