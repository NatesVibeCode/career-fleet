import time

from harness_fleet.catalog import RouteCatalog
from harness_fleet.cli import build_parser, cmd_cooldowns
from harness_fleet.mcp_server import create_mcp_server
from harness_fleet.store import HarnessStore


def test_adaptive_backoff_progression(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    route_id = "test-provider/test-model"

    # Initially 0 consecutive rate limits
    assert store.get_consecutive_rate_limits(route_id) == 0

    # 1st rate limit: base 30s (+ jitter 0-5s)
    exp1 = store.record_rate_limit_with_adaptive_backoff(route_id, reason="Rate limit 1")
    now = time.time()
    assert 29 <= (exp1 - now) <= 36
    store.record_inference_attempt({
        "attempt_id": "att-1",
        "route_id": route_id,
        "outcome": "rate_limited",
        "error_type": "rate_limit",
        "verified": 0,
    })
    assert store.get_consecutive_rate_limits(route_id) == 1

    # 2nd rate limit: base 60s (+ jitter)
    exp2 = store.record_rate_limit_with_adaptive_backoff(route_id, reason="Rate limit 2")
    now = time.time()
    assert 59 <= (exp2 - now) <= 67
    store.record_inference_attempt({
        "attempt_id": "att-2",
        "route_id": route_id,
        "outcome": "rate_limited",
        "error_type": "rate_limit",
        "verified": 0,
    })
    assert store.get_consecutive_rate_limits(route_id) == 2

    # 3rd rate limit: base 180s (+ jitter)
    exp3 = store.record_rate_limit_with_adaptive_backoff(route_id, reason="Rate limit 3")
    now = time.time()
    assert 179 <= (exp3 - now) <= 196
    store.record_inference_attempt({
        "attempt_id": "att-3",
        "route_id": route_id,
        "outcome": "rate_limited",
        "error_type": "rate_limit",
        "verified": 0,
    })
    assert store.get_consecutive_rate_limits(route_id) == 3

    # 4th rate limit: max 600s (+ jitter)
    exp4 = store.record_rate_limit_with_adaptive_backoff(route_id, reason="Rate limit 4")
    now = time.time()
    assert 599 <= (exp4 - now) <= 635
    store.record_inference_attempt({
        "attempt_id": "att-4",
        "route_id": route_id,
        "outcome": "rate_limited",
        "error_type": "rate_limit",
        "verified": 0,
    })
    assert store.get_consecutive_rate_limits(route_id) == 4

    # Now simulate a successful verified attempt
    store.record_inference_attempt({
        "attempt_id": "att-5",
        "route_id": route_id,
        "outcome": "verified",
        "verified": 1,
    })
    # Count of consecutive rate limits should now be reset to 0
    assert store.get_consecutive_rate_limits(route_id) == 0

    # Next rate limit starts back at 30s
    exp5 = store.record_rate_limit_with_adaptive_backoff(route_id, reason="Rate limit after reset")
    now = time.time()
    assert 29 <= (exp5 - now) <= 36
    store.record_inference_attempt({
        "attempt_id": "att-6",
        "route_id": route_id,
        "outcome": "rate_limited",
        "error_type": "rate_limit",
        "verified": 0,
    })
    assert store.get_consecutive_rate_limits(route_id) == 1


def test_cooldown_details_and_clearing(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    store.record_rate_limit_with_adaptive_backoff("provider/model-a", reason="Hit quota A")
    store.record_rate_limit_with_adaptive_backoff("provider/model-b", reason="Hit quota B")

    details = store.get_active_cooldown_details()
    assert len(details) == 2
    r_ids = {d["route_id"] for d in details}
    assert r_ids == {"provider/model-a", "provider/model-b"}

    # Clear one route
    cleared = store.clear_cooldowns(route_id="provider/model-a")
    assert cleared == 1
    details_after = store.get_active_cooldown_details()
    assert len(details_after) == 1
    assert details_after[0]["route_id"] == "provider/model-b"

    # Clear all routes
    cleared_all = store.clear_cooldowns()
    assert cleared_all == 1
    assert len(store.get_active_cooldown_details()) == 0


def test_route_summary_with_cooldowns(tmp_path):
    db_path = tmp_path / "test.db"
    _store = HarnessStore(db_path)
    catalog = RouteCatalog(db_path=db_path)
    catalog.add_route(
        route_id="mock/r1",
        provider="mock",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state="price_observed_zero",
    )
    catalog.set_cooldown("mock/r1", duration_sec=60.0, reason="test cooldown")

    summary = catalog.get_route_summary()
    assert len(summary) >= 1
    r1 = next(r for r in summary if r["id"] == "mock/r1")
    assert r1["is_cooling"] is True
    assert r1["cooling_seconds"] > 0


def test_cli_cooldowns_command(tmp_path, capsys):
    db_path = tmp_path / "test.db"
    store = HarnessStore(db_path)
    store.record_rate_limit_with_adaptive_backoff("mock/m1", reason="429 error")

    parser = build_parser()

    # Test list cooldowns JSON
    args = parser.parse_args(["cooldowns", "--db", str(db_path), "--json"])
    cmd_cooldowns(args)
    captured = capsys.readouterr()
    import json
    data = json.loads(captured.out)
    assert data["count"] == 1
    assert data["cooldowns"][0]["route_id"] == "mock/m1"

    # Test clear cooldowns JSON
    args_clear = parser.parse_args(["cooldowns", "--db", str(db_path), "--json", "--clear", "--route", "mock/m1"])
    cmd_cooldowns(args_clear)
    captured_clear = capsys.readouterr()
    data_clear = json.loads(captured_clear.out)
    assert data_clear["cleared"] == 1
    assert data_clear["route_id"] == "mock/m1"


def test_mcp_cooldowns_tool(tmp_path):
    server = create_mcp_server(workspace_root=tmp_path, db_path="test.db")
    tool_names = [t.name for t in server._tool_manager.list_tools()]
    assert "harness_fleet_cooldowns" in tool_names
