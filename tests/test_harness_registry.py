"""Explicit-only provider resolution, availability matrix, doctor, refresh shape."""
import json

import pytest

from harness_fleet.catalog import RouteCatalog
from harness_fleet.providers.registry import (
    HARNESS_SPECS,
    ProviderRegistry,
    ProviderResolutionError,
    configured_routes,
)


def test_seven_equal_harness_providers_registered():
    registry = ProviderRegistry()
    assert [spec.name for spec in HARNESS_SPECS] == [
        "opencode", "claude", "codex", "cursor", "grok", "muse", "antigravity",
    ]
    for spec in HARNESS_SPECS:
        assert registry.get(spec.name) is not None


def test_explicit_resolve_all_seven():
    registry = ProviderRegistry()
    for spec in HARNESS_SPECS:
        assert registry.resolve(spec.name) is registry.get(spec.name)


def test_unknown_provider_fails_closed_with_available_names():
    registry = ProviderRegistry()
    for bad in (None, "", "nope", "opencode/anthropic/claude"):
        with pytest.raises(ProviderResolutionError) as exc_info:
            registry.resolve(bad)
        for spec in HARNESS_SPECS:
            assert spec.name in str(exc_info.value)


def test_resolve_accepts_route_id_by_provider():
    from harness_fleet.models import RouteId

    registry = ProviderRegistry()
    assert registry.resolve(RouteId.parse("codex/some-model")) is registry.get("codex")
    with pytest.raises(ProviderResolutionError):
        registry.resolve(RouteId(provider="nope", model="x"))


def test_no_prefix_substring_or_default_resolution():
    registry = ProviderRegistry()
    # Route ids never dispatch: only the explicit provider field resolves.
    with pytest.raises(ProviderResolutionError):
        registry.resolve(None)
    with pytest.raises(ProviderResolutionError):
        registry.resolve("openrouter-free-zone")


def test_configured_routes_matrix_is_binary_on_path_for_harnesses(monkeypatch):
    import harness_fleet.providers.harness as harness_module

    present = {"opencode", "cursor-agent"}
    monkeypatch.setattr(harness_module.shutil, "which", lambda name: f"/bin/{name}" if name in present else None)
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    routes = [
        {"id": "opencode/m", "provider": "opencode"},
        {"id": "claude/m", "provider": "claude"},
        {"id": "cursor/m", "provider": "cursor"},
        {"id": "openrouter/m", "provider": "openrouter"},
        {"id": "demo/fake", "provider": "demo"},
        {"id": "x/y", "provider": "unknown"},
    ]
    ids = {route["id"] for route in configured_routes(routes)}
    assert ids == {"opencode/m", "cursor/m"}


def test_doctor_reports_per_harness_matrix(tmp_path, monkeypatch, capsys):
    import shutil as _shutil

    from harness_fleet.cli import build_parser, cmd_doctor

    monkeypatch.setattr(_shutil, "which", lambda name: f"/bin/{name}" if name == "opencode" else None)
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    args = build_parser().parse_args(["doctor", "--workspace-root", str(tmp_path), "--json"])
    with pytest.raises(SystemExit):
        cmd_doctor(args)
    report = json.loads(capsys.readouterr().out)
    names = {check["name"] for check in report["checks"]}
    for spec in HARNESS_SPECS:
        assert spec.name in names
    by_name = {check["name"]: check for check in report["checks"]}
    assert by_name["opencode"]["ok"] is True
    assert by_name["claude"]["ok"] is False
    assert "not found in PATH" in by_name["claude"]["detail"]


def test_refresh_all_shape_with_discovery_zeros(tmp_path, monkeypatch):
    import harness_fleet.catalog as catalog_module

    monkeypatch.setattr(catalog_module.shutil, "which", lambda name: None)

    class NoNet:
        def __init__(self, *args, **kwargs):
            raise RuntimeError("offline")

    monkeypatch.setattr(catalog_module.httpx, "Client", NoNet)
    catalog = RouteCatalog(db_path=tmp_path / "fleet.db")
    results = catalog.refresh_all()
    for spec in HARNESS_SPECS:
        assert spec.name in results or f"{spec.name}_error" in results
    for name in ("claude", "codex", "grok", "muse", "antigravity"):
        assert results[name] == 0
    assert "opencode_error" in results


def test_refresh_from_harness_cursor_discovers_candidates(tmp_path, monkeypatch):
    import subprocess as _sp

    import harness_fleet.catalog as catalog_module
    from harness_fleet.providers.cursor import CURSOR_SPEC

    monkeypatch.setattr(catalog_module.shutil, "which", lambda name: "/bin/cursor-agent")

    def fake_run(argv, capture_output, text, timeout):
        assert argv[:2] == ["cursor-agent", "models"]
        return _sp.CompletedProcess(argv, 0, stdout=json.dumps([{"id": "pro-1"}]), stderr="")

    monkeypatch.setattr(catalog_module.subprocess, "run", fake_run)
    catalog = RouteCatalog(db_path=tmp_path / "fleet.db")
    assert catalog.refresh_from_harness(CURSOR_SPEC) == 1
    routes = {route["id"]: route for route in catalog.data["routes"] if route["provider"] == "cursor"}
    assert routes["cursor/pro-1"]["price_state"] == "candidate"
    assert routes["cursor/pro-1"]["enabled"] is False


def test_refresh_from_harness_discovery_less_is_zero():
    from harness_fleet.providers.claude import CLAUDE_SPEC

    catalog = RouteCatalog.__new__(RouteCatalog)
    assert catalog.refresh_from_harness(CLAUDE_SPEC) == 0
