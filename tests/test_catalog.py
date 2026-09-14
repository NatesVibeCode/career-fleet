import httpx
import pytest

from harness_fleet.catalog import (
    PriceState,
    RouteCatalog,
    RouteCircuitBreaker,
    classify_price_state,
)
from harness_fleet.models import RoutePolicy


def test_catalog_ladder_rotation(tmp_path):
    cfg = tmp_path / "routes.json"
    cat = RouteCatalog(config_path=cfg)
    cat.data = {
        "revision": 1,
        "routes": [
            {"id": "r1", "enabled": True, "price_state": "price_observed_zero"},
            {"id": "r2", "enabled": True, "price_state": "price_observed_zero"},
            {"id": "r3", "enabled": True, "price_state": "price_observed_zero"},
        ]
    }
    cat.save()

    l1 = cat.get_ladder(task_seed="seed_a", free_only=True)
    assert len(l1) == 3
    assert set(l1) == {"r1", "r2", "r3"}

def test_circuit_breaker_on_nonzero_cost(tmp_path):
    cfg = tmp_path / "routes.json"
    cat = RouteCatalog(config_path=cfg)
    cat.data = {
        "revision": 1,
        "routes": [
            {"id": "r_free", "enabled": True, "price_state": "price_observed_zero"}
        ]
    }
    cat.save()

    # Reporting non-zero cost must trip circuit breaker
    with pytest.raises(RouteCircuitBreaker):
        cat.record_cost("r_free", reported_cost=0.005)
    
    # Check that route was disabled in catalog
    assert cat.data["routes"][0]["enabled"] is False
    assert "Circuit breaker tripped" in cat.data["routes"][0]["disabled_reason"]

def test_price_state_requires_observed_pricing():
    assert classify_price_state({"id": "meta/llama:free"}) is PriceState.CANDIDATE
    assert classify_price_state({"id": "m1", "name": "Model 1 (Free tier)"}) is PriceState.CANDIDATE
    assert classify_price_state({"id": "m2", "pricing": {"prompt": "0", "completion": "0"}}) is PriceState.PRICE_OBSERVED_ZERO
    assert classify_price_state({"id": "m3", "cost": {"input": 0, "output": 0}}) is PriceState.PRICE_OBSERVED_ZERO
    assert classify_price_state({"id": "m4", "pricing": {"prompt": "0.001", "completion": "0.002"}}) is PriceState.UNKNOWN


def test_candidate_route_is_not_in_free_ladder(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {"revision": 2, "routes": [{"id": "looks-free", "enabled": True, "price_state": "candidate"}]}
    assert cat.get_ladder(free_only=True) == []


def test_packaged_routes_are_disabled_hints_until_refreshed(tmp_path):
    catalog = RouteCatalog(db_path=tmp_path / "state.db")
    assert catalog.get_routes(free_only=True) == []
    assert catalog.get_routes(free_only=False, include_disabled=True)


def test_add_route_explicit_registration(tmp_path):
    catalog = RouteCatalog(db_path=tmp_path / "state.db")
    route = catalog.add_route(
        route_id="custom/my-model",
        provider="openai_compatible",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state="price_observed_zero",
    )
    assert route.id == "custom/my-model"
    assert route.provider == "openai_compatible"
    assert route.price_state == "price_observed_zero"

    routes = catalog.get_routes(free_only=True)
    assert any(r["id"] == "custom/my-model" for r in routes)


def test_refresh_from_openai_compatible_local_is_free(tmp_path, monkeypatch):
    catalog = RouteCatalog(db_path=tmp_path / "state.db")

    class MockResponse:
        status_code = 200
        def json(self):
            return {"data": [{"id": "llama3.2"}, {"id": "mistral"}]}

    monkeypatch.setattr(
        httpx.Client,
        "get",
        lambda *args, **kwargs: MockResponse(),
    )

    count = catalog.refresh_from_openai_compatible(
        base_url="http://localhost:11434/v1",
    )
    assert count == 2
    free_routes = catalog.get_routes(free_only=True)
    route_ids = {r["id"] for r in free_routes}
    assert "openai_compatible/llama3.2" in route_ids
    assert "openai_compatible/mistral" in route_ids


def test_record_cost_paid_policy_within_spend_limit(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [
            {"id": "r_paid", "enabled": True, "price_state": "unknown", "cost_per_1k_input": 1.0, "cost_per_1k_output": 2.0}
        ]
    }
    cat.save()

    policy = RoutePolicy(
        allowed_routes=["r_paid"],
        max_cost_per_1k_input=2.0,
        max_cost_per_1k_output=3.0,
        max_request_cost=0.10,
    )
    cat.record_cost("r_paid", reported_cost=0.025, policy=policy)

    assert cat.data["routes"][0]["enabled"] is True
    assert cat.data["routes"][0]["last_price_observation"] is not None
    assert cat.data["routes"][0]["last_price_observation"] > 0


def test_record_cost_paid_policy_exceeding_max_request_cost(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [
            {"id": "r_paid", "enabled": True, "price_state": "unknown", "cost_per_1k_input": 1.0, "cost_per_1k_output": 2.0}
        ]
    }
    cat.save()

    policy = RoutePolicy(
        allowed_routes=["r_paid"],
        max_cost_per_1k_input=2.0,
        max_cost_per_1k_output=3.0,
        max_request_cost=0.01,
    )
    with pytest.raises(RouteCircuitBreaker) as exc:
        cat.record_cost("r_paid", reported_cost=0.05, policy=policy)

    assert "exceeded policy max_request_cost" in str(exc.value)
    assert cat.data["routes"][0]["enabled"] is False
    assert "max_request_cost" in cat.data["routes"][0]["disabled_reason"]


def test_zero_cost_receipt_does_not_promote_paid_route(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [{
            "id": "r_paid",
            "enabled": True,
            "price_state": "unknown",
            "cost_per_1k_input": 1.0,
            "cost_per_1k_output": 2.0,
        }],
    }
    cat.save()

    cat.record_cost("r_paid", reported_cost=0.0)

    assert cat.data["routes"][0]["price_state"] == PriceState.UNKNOWN.value
    assert "r_paid" not in cat.get_ladder(free_only=True)


def test_zero_price_route_rejects_nonzero_declared_cost(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")

    with pytest.raises(ValueError, match="cannot declare non-zero token costs"):
        cat.add_route(
            "r_bad_free",
            provider="paid",
            cost_per_1k_input=1.0,
            cost_per_1k_output=2.0,
            price_state=PriceState.PRICE_OBSERVED_ZERO.value,
        )


def test_legacy_inconsistent_free_route_is_not_selected(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [{
            "id": "legacy/paid",
            "enabled": True,
            "price_state": PriceState.PRICE_OBSERVED_ZERO.value,
            "cost_per_1k_input": 1.0,
            "cost_per_1k_output": 2.0,
        }],
    }
    cat.save()

    assert cat.get_ladder(free_only=True) == []


def test_openrouter_refresh_disables_stale_free_route(tmp_path, monkeypatch):
    class FakeClient:
        def __init__(self, *args, **kwargs):
            pass

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

        def get(self, url, **kwargs):
            return httpx.Response(200, json={"data": []})

    monkeypatch.setattr("httpx.Client", FakeClient)
    cat = RouteCatalog(db_path=tmp_path / "state.db")
    cat.add_route(
        "openrouter/old-model",
        provider="openrouter",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        price_state=PriceState.PRICE_OBSERVED_ZERO.value,
    )

    cat.refresh_from_openrouter()

    route = next(r for r in cat.data["routes"] if r["id"] == "openrouter/old-model")
    assert route["enabled"] is False
    assert route["price_state"] == PriceState.UNKNOWN.value
    assert cat.get_ladder(free_only=True) == []


def test_cost_limits_do_not_approve_paid_routes(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [
            {"id": "r_paid", "enabled": True, "price_state": "unknown", "cost_per_1k_input": 1.0, "cost_per_1k_output": 2.0},
            {"id": "r_free", "enabled": True, "price_state": "price_observed_zero"},
        ],
    }
    cat.save()

    policy = RoutePolicy(max_cost_per_1k_input=2.0, max_cost_per_1k_output=3.0, max_request_cost=0.10)
    ladder = cat.get_ladder(policy=policy, free_only=True)

    assert "r_paid" not in ladder
    assert "r_free" in ladder


def test_explicit_route_approval_allows_paid_lane(tmp_path):
    cat = RouteCatalog(config_path=tmp_path / "routes.json")
    cat.data = {
        "revision": 1,
        "routes": [{
            "id": "r_paid",
            "enabled": True,
            "price_state": "unknown",
            "cost_per_1k_input": 1.0,
            "cost_per_1k_output": 2.0,
        }],
    }
    cat.save()

    ladder = cat.get_ladder(
        policy=RoutePolicy(allowed_routes=["r_paid"]),
        free_only=True,
    )

    assert ladder == ["r_paid"]


def test_add_route_cost_safety_defaults(tmp_path):
    cat = RouteCatalog(db_path=tmp_path / "state.db")
    route = cat.add_route(
        route_id="groq/llama-3.3-70b-versatile",
        provider="groq",
    )
    assert route.price_state == PriceState.UNKNOWN.value
    assert route.cost_per_1k_input is None
    assert route.cost_per_1k_output is None
