import json

from harness_fleet.catalog import RouteCatalog
from harness_fleet.eval import RouteEvaluator
from harness_fleet.models import InputItem, TaskSpec
from harness_fleet.store import HarnessStore


class EvalMockProvider:
    def __init__(self, high_quality_route="route/strong"):
        self.high_quality_route = high_quality_route

    def run_prompt(self, route_id, prompt, system_prompt=None, timeout_sec=120, session_id=None, **kwargs):
        if route_id == self.high_quality_route:
            # Good response with valid schema and quote
            payload = {
                "items": [{
                    "item_id": "item-eval-1",
                    "claims": {"sentiment": "positive"},
                    "quotes": [{"slice_id": "full", "start": 0, "end": 20, "text": "excellent product 12"}],
                }]
            }
            return True, json.dumps(payload), {
                "id": "rec-good",
                "session_id": session_id,
                "provider": "mock",
                "requested_route": route_id,
                "status": "complete",
                "cost": 0.0,
                "cost_status": "reported_zero",
                "usage": {"total_tokens": 15},
                "duration_seconds": 0.5,
            }
        else:
            # Low quality response with hallucinated quotes
            payload = {
                "items": [{
                    "item_id": "item-eval-1",
                    "claims": {"sentiment": "positive"},
                    "quotes": [{"slice_id": "full", "start": 0, "end": 20, "text": "hallucinated quote text"}],
                }]
            }
            return True, json.dumps(payload), {
                "id": "rec-bad",
                "session_id": session_id,
                "provider": "mock",
                "requested_route": route_id,
                "status": "complete",
                "cost": 0.0,
                "cost_status": "reported_zero",
                "usage": {"total_tokens": 15},
                "duration_seconds": 2.5,
            }


def test_route_eval_and_feedback_loop(tmp_path):
    db_path = tmp_path / "test.db"
    store = HarnessStore(db_path)
    catalog = RouteCatalog(config_path=tmp_path / "routes.json", db_path=db_path)
    catalog.data = {
        "revision": 2,
        "routes": [
            {"id": "route/weak", "provider": "mock", "enabled": True, "price_state": "price_observed_zero"},
            {"id": "route/strong", "provider": "mock", "enabled": True, "price_state": "price_observed_zero"},
        ],
    }
    catalog.save()

    task = TaskSpec(
        name="sentiment-eval",
        min_quote_chars=5,
        claims_schema={
            "type": "object",
            "properties": {"sentiment": {"type": "string"}},
            "required": ["sentiment"],
            "additionalProperties": False,
        },
    )

    evaluator = RouteEvaluator(task=task, store=store, catalog=catalog)
    mock_prov = EvalMockProvider(high_quality_route="route/strong")
    evaluator.registry.register("mock", mock_prov)

    samples = [
        InputItem(
            item_id="item-eval-1",
            text="excellent product 12345",
            metadata={"expected_sentiment": "positive"},
        )
    ]

    report = evaluator.evaluate_all(
        samples=samples,
        routes=["route/weak", "route/strong"],
        expected_claims_key="expected_sentiment",
    )

    assert len(report.routes) == 2
    strong_result = next(r for r in report.routes if r.route_id == "route/strong")
    weak_result = next(r for r in report.routes if r.route_id == "route/weak")

    assert strong_result.composite_score > weak_result.composite_score
    assert strong_result.grounding_pass_count == 1
    assert weak_result.grounding_pass_count == 0

    # Verify closed loop: get_ladder now prioritizes route/strong for this task!
    ladder = catalog.get_ladder(task_name=task.name, free_only=True)
    assert ladder[0] == "route/strong"
