import time

from harness_fleet.models import RoutePolicy
from harness_fleet.scoring import RouteScorer, filter_and_rank_routes
from harness_fleet.store import HarnessStore


def test_route_scoring_ranks_verified_over_failed(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    scorer = RouteScorer(store)

    routes = [
        {"id": "route/good", "provider": "openrouter", "enabled": True},
        {"id": "route/bad", "provider": "openrouter", "enabled": True},
    ]

    # Insert fake model runs
    with store.connect() as conn:
        for i in range(10):
            conn.execute(
                """INSERT INTO model_runs(
                    receipt_id, run_id, batch_id, provider, requested_route, status,
                    cost, cost_status, error, duration_seconds, receipt_json, created_at
                ) VALUES(?, 'r1', 'b1', 'openrouter', 'route/good', 'complete', 0, 'reported_zero', NULL, 1.2, '{}', '2026-09-09')""",
                (f"rec-good-{i}",),
            )
        for i in range(10):
            conn.execute(
                """INSERT INTO model_runs(
                    receipt_id, run_id, batch_id, provider, requested_route, status,
                    cost, cost_status, error, duration_seconds, receipt_json, created_at
                ) VALUES(?, 'r1', 'b1', 'openrouter', 'route/bad', 'failed', 0, 'reported_zero', 'malformed JSON', 5.0, '{}', '2026-09-09')""",
                (f"rec-bad-{i}",),
            )

    scores = scorer.score_routes(routes)
    assert scores["route/good"] > scores["route/bad"]
    assert scores["route/good"] > 0.6
    assert scores["route/bad"] < 0.3


def test_route_cooldown_zeros_score(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    scorer = RouteScorer(store)

    routes = [{"id": "route/cooled", "provider": "openrouter", "enabled": True}]
    store.set_cooldown("route/cooled", cooldown_until=time.time() + 60, reason="429")

    scores = scorer.score_routes(routes)
    assert scores["route/cooled"] == 0.0


def test_filter_and_rank_routes_respects_policy(tmp_path):
    store = HarnessStore(tmp_path / "test.db")
    routes = [
        {"id": "openrouter/free-1", "provider": "openrouter", "enabled": True},
        {"id": "opencode/local-1", "provider": "opencode", "enabled": True},
        {"id": "ollama/qwen", "provider": "ollama", "enabled": True},
    ]

    # Policy allowing only opencode and ollama
    policy = RoutePolicy(allowed_providers=["opencode", "ollama"])
    ranked = filter_and_rank_routes(routes, store=store, policy=policy)
    assert "openrouter/free-1" not in ranked
    assert set(ranked) == {"opencode/local-1", "ollama/qwen"}

    # Policy excluding ollama
    policy_ex = RoutePolicy(excluded_providers=["ollama"])
    ranked_ex = filter_and_rank_routes(routes, store=store, policy=policy_ex)
    assert "ollama/qwen" not in ranked_ex
    assert set(ranked_ex) == {"openrouter/free-1", "opencode/local-1"}
