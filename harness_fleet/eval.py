"""Continuous automated evaluation harness measuring route capabilities across free and local models."""
from __future__ import annotations

import time
import uuid
from collections.abc import Callable, Iterable, Iterator
from datetime import datetime, timezone
from itertools import islice

from pydantic import ValidationError

from .catalog import RouteCatalog
from .grounding import normalize_grounding
from .models import (
    CandidateModelOutput,
    InputItem,
    RouteEvalReport,
    RouteEvalResult,
    TaskSpec,
    coerce_receipt,
)
from .providers.base import clean_llm_json
from .providers.registry import ProviderRegistry, ProviderResolutionError
from .store import HarnessStore


class RouteEvaluator:
    def __init__(
        self,
        task: TaskSpec,
        store: HarnessStore | None = None,
        catalog: RouteCatalog | None = None,
        registry: ProviderRegistry | None = None,
    ):
        self.task = task
        self.store = store or HarnessStore()
        self.catalog = catalog or RouteCatalog(db_path=self.store.path)
        self.registry = registry or ProviderRegistry()

    def evaluate_route(
        self,
        route_id: str,
        samples: Iterable[InputItem],
        expected_claims_key: str | None = None,
        concurrency: int = 4,
    ) -> RouteEvalResult:
        if concurrency < 1:
            raise ValueError("concurrency must be greater than 0")
        routes_by_id = {r["id"]: r for r in self.catalog.data.get("routes", [])}
        route_info = routes_by_id.get(route_id, {})
        provider_hint = route_info.get("provider")
        try:
            provider = self.registry.resolve(provider_hint)
        except ProviderResolutionError as exc:
            raise ValueError(f"cannot evaluate route '{route_id}': {exc}") from exc

        total = 0
        sample_iterator = iter(samples)
        # Threaded per-sample evaluation for throughput, with a bounded input window.
        import concurrent.futures as _cf

        def _eval_one(item: InputItem) -> dict:
            scored_at = datetime.now(timezone.utc).isoformat()
            captured_at = item.metadata.get("captured_at")
            prompt = self.task.render_prompt([{
                "item_id": item.item_id,
                "title": item.title or "",
                "sections": [{
                    "slice_id": "full",
                    "start": 0,
                    "end": len(item.text),
                    "text": item.text,
                }]
            }])
            ok, response_text, receipt = provider.run_prompt(
                route_id=route_id,
                prompt=prompt,
                system_prompt=self.task.render_instructions(),
            )
            return {
                "ok": ok, "response_text": response_text, "receipt": receipt, "item": item,
                "scored_at": scored_at,
                "captured_at": captured_at if isinstance(captured_at, str) else None,
            }

        def _result_stream() -> Iterator[dict]:
            nonlocal total
            window_size = max(1, concurrency)
            while True:
                window = list(islice(sample_iterator, window_size))
                if not window:
                    return
                total += len(window)
                if concurrency <= 1:
                    for item in window:
                        yield _eval_one(item)
                else:
                    with _cf.ThreadPoolExecutor(max_workers=min(concurrency, len(window))) as ex:
                        yield from ex.map(_eval_one, window)

        # Aggregate
        schema_passed = 0
        grounding_passed = 0
        correct_count = 0
        rate_limits = 0
        errors = 0
        duration_sum = 0.0
        duration_count = 0
        bias_error_sum = 0.0
        bias_error_count = 0

        for res in _result_stream():
            item = res["item"]
            ok = res["ok"]
            response_text = res["response_text"]
            try:
                receipt = coerce_receipt(res["receipt"])
            except ValidationError as exc:
                errors += 1
                self.store.record_inference_attempt({
                    "attempt_id": f"eval:{route_id}:{item.item_id}:{uuid.uuid4().hex[:8]}",
                    "run_id": f"eval:{self.task.name}",
                    "batch_id": item.item_id,
                    "lease_attempt_number": 1,
                    "route_id": route_id,
                    "provider": provider_hint or "unknown",
                    "task_name": self.task.name,
                    "started_at": datetime.now(timezone.utc).isoformat(),
                    "duration_seconds": None,
                    "cost": None,
                    "cost_status": "unknown",
                    "usage": None,
                    "retry_after": None,
                    "error_type": "invalid_receipt",
                    "error_message": f"Invalid provider receipt from '{route_id}': {exc}",
                    "transport_status": "failed",
                    "parse_status": "skipped",
                    "schema_status": "skipped",
                    "grounding_status": "skipped",
                    "outcome": "transport_failed",
                    "verified": False,
                    "counts_against_budget": 1,
                })
                continue

            dur = receipt.duration_seconds
            if dur is not None and dur > 0:
                duration_sum += float(dur)
                duration_count += 1

            attempt_rec = {
                "attempt_id": f"eval:{route_id}:{item.item_id}:{uuid.uuid4().hex[:8]}",
                "run_id": f"eval:{self.task.name}",
                "batch_id": item.item_id,
                "lease_attempt_number": 1,
                "route_id": route_id,
                "provider": receipt.provider or provider_hint or "unknown",
                "task_name": self.task.name,
                "started_at": datetime.now(timezone.utc).isoformat(),
                "duration_seconds": receipt.duration_seconds,
                "cost": receipt.cost,
                "cost_status": receipt.cost_status,
                "usage": receipt.usage,
                "retry_after": receipt.retry_after,
                "error_type": receipt.error_type,
                "error_message": receipt.error,
                "transport_status": "success",
                "parse_status": "skipped",
                "schema_status": "skipped",
                "grounding_status": "skipped",
                "outcome": "failed",
                "verified": False,
                "counts_against_budget": 1,
            }

            if not ok:
                errors += 1
                is_rl = receipt.error_type == "rate_limit" or "429" in str(receipt.error or "")
                if is_rl:
                    rate_limits += 1
                attempt_rec.update({
                    "transport_status": "rate_limit" if is_rl else "failed",
                    "outcome": "rate_limited" if is_rl else "transport_failed",
                    "counts_against_budget": 0 if is_rl else 1,
                })
                self.store.record_inference_attempt(attempt_rec)
                continue

            parsed = clean_llm_json(response_text)
            if not parsed or not isinstance(parsed, dict):
                errors += 1
                attempt_rec.update({
                    "transport_status": "success",
                    "parse_status": "malformed_json",
                    "outcome": "parse_failed",
                    "error_message": "Malformed JSON",
                })
                self.store.record_inference_attempt(attempt_rec)
                continue

            try:
                candidate = CandidateModelOutput.model_validate(parsed)
                for extracted in candidate.items:
                    self.task.validate_claims(
                        extracted.claims,
                        quotes=extracted.quotes,
                        strengths=self.task.support_strengths(
                            extracted.quotes, item.source_uri,
                            res.get("captured_at"), res.get("scored_at"),
                        ),
                    )
                schema_passed += 1
            except (ValidationError, ValueError) as exc:
                errors += 1
                attempt_rec.update({
                    "transport_status": "success",
                    "parse_status": "success",
                    "schema_status": "schema_violation",
                    "outcome": "schema_failed",
                    "error_message": str(exc),
                })
                self.store.record_inference_attempt(attempt_rec)
                continue

            import hashlib
            source_digest = hashlib.sha256(item.text.encode()).hexdigest()
            output_items, ground_err = normalize_grounding(
                extracted_items=candidate.items,
                raw_cards=[{
                    "item_id": item.item_id,
                    "source_digest": source_digest,
                    "content_type": item.content_type,
                    "metadata": dict(item.metadata),
                    "slices": [{"slice_id": "full", "start": 0, "end": len(item.text), "text": item.text}],
                }],
                min_quote_chars=self.task.min_quote_chars,
                evidence_terms=self.task.evidence_terms,
                candidate_top_n=self.task.candidate_top_n,
                scored_at=res.get("scored_at"),
            )

            if output_items is None:
                errors += 1
                attempt_rec.update({
                    "transport_status": "success",
                    "parse_status": "success",
                    "schema_status": "success",
                    "grounding_status": "grounding_failed",
                    "outcome": "grounding_failed",
                    "error_message": ground_err,
                })
                self.store.record_inference_attempt(attempt_rec)
                continue

            grounding_passed += 1
            attempt_rec.update({
                "transport_status": "success",
                "parse_status": "success",
                "schema_status": "success",
                "grounding_status": "success",
                "outcome": "verified",
                "verified": True,
            })
            self.store.record_inference_attempt(attempt_rec)

            # Check correctness if expected claims provided in item metadata.
            # Accuracy stays exact (no tolerance): tolerance would inflate small
            # scales (e.g. 1-5 Likert) where any answer is within 5. Numeric pairs
            # are separately recorded as rater bias for export calibration.
            if expected_claims_key and expected_claims_key in item.metadata:
                expected_raw = item.metadata[expected_claims_key]
                # Correctness compares pipeline-derived claims (score, tier,
                # passed) so checklist tasks are judged on what ships.
                try:
                    actual_claims = self.task.with_derived_claims(
                        dict(candidate.items[0].claims),
                        self.task.support_strengths(
                            candidate.items[0].quotes, item.source_uri,
                            res.get("captured_at"), res.get("scored_at"),
                        ),
                    )
                except ValueError:
                    actual_claims = candidate.items[0].claims
                if isinstance(expected_raw, dict):
                    if actual_claims == expected_raw:
                        correct_count += 1
                else:
                    try:
                        bias_error_sum += (
                            float(actual_claims.get(expected_claims_key, "")) - float(expected_raw)  # type: ignore[arg-type]
                        )
                        bias_error_count += 1
                    except (ValueError, TypeError):
                        pass
                    if str(actual_claims.get(expected_claims_key, "")).lower() == str(expected_raw).lower():
                        correct_count += 1

        avg_lat = (duration_sum / duration_count) if duration_count else 0.0
        schema_rate = (schema_passed / total) if total > 0 else 0.0
        grounding_rate = (grounding_passed / total) if total > 0 else 0.0
        accuracy = (correct_count / total) if (expected_claims_key and total > 0) else None

        if total <= 0:
            comp = 0.0
        else:
            # Latency score normalized (0-30s)
            lat_score = max(0.1, 1.0 - (min(30.0, avg_lat) / 30.0) * 0.5)

            # Composite score
            if accuracy is not None:
                comp = 0.35 * schema_rate + 0.35 * grounding_rate + 0.20 * accuracy + 0.10 * lat_score
            else:
                comp = 0.45 * schema_rate + 0.45 * grounding_rate + 0.10 * lat_score

            if rate_limits > 0:
                comp *= max(0.2, 1.0 - (rate_limits / total) * 0.5)

            comp = round(max(0.0, min(1.0, comp)), 3)

        result = RouteEvalResult(
            route_id=route_id,
            provider=provider_hint or "unknown",
            total_samples=total,
            schema_pass_count=schema_passed,
            grounding_pass_count=grounding_passed,
            correct_count=correct_count if expected_claims_key else None,
            rate_limit_count=rate_limits,
            error_count=errors,
            schema_pass_rate=round(schema_rate, 3),
            grounding_pass_rate=round(grounding_rate, 3),
            accuracy=round(accuracy, 3) if accuracy is not None else None,
            avg_latency_seconds=round(avg_lat, 2),
            composite_score=comp,
        )

        # Persist to database to immediately steer future routing decisions
        self.store.record_route_eval({
            "task_name": self.task.name,
            "route_id": route_id,
            "provider": provider_hint or "unknown",
            "total_samples": total,
            "schema_pass_count": schema_passed,
            "grounding_pass_count": grounding_passed,
            "correct_count": correct_count if expected_claims_key else None,
            "rate_limit_count": rate_limits,
            "error_count": errors,
            "avg_latency_seconds": avg_lat,
            "composite_score": comp,
        })
        # Persist rater bias for bias-adjusted export ranking (numeric goldens only).
        if bias_error_count and hasattr(self.store, "update_route_claim_bias_stats"):
            try:
                self.store.update_route_claim_bias_stats(
                    self.task.name, route_id, bias_error_sum, bias_error_count
                )
            except Exception:
                pass

        return result

    def evaluate_all(
        self,
        samples: Iterable[InputItem],
        routes: list[str] | None = None,
        expected_claims_key: str | None = None,
        concurrency: int = 4,
        samples_factory: Callable[[], Iterable[InputItem]] | None = None,
    ) -> RouteEvalReport:
        if concurrency < 1:
            raise ValueError("concurrency must be greater than 0")
        target_routes = routes or self.catalog.get_ladder(free_only=True)
        if samples_factory is None:
            sample_iterator = iter(samples)
            if sample_iterator is samples and len(target_routes) > 1:
                raise ValueError("samples_factory is required for one-shot samples across multiple routes")
            sample_factory: Callable[[], Iterable[InputItem]] = (lambda: sample_iterator) if sample_iterator is samples else (lambda: iter(samples))
        else:
            sample_factory = samples_factory
        results = []
        # Parallelize across routes as well when multiple routes
        if len(target_routes) > 1 and concurrency > 1:
            import concurrent.futures as _cf
            with _cf.ThreadPoolExecutor(max_workers=min(len(target_routes), 4)) as ex:
                futures = {
                    ex.submit(self.evaluate_route, rid, sample_factory(), expected_claims_key, 1): rid
                    for rid in target_routes
                }
                for fut in _cf.as_completed(futures):
                    results.append(fut.result())
        else:
            for rid in target_routes:
                results.append(self.evaluate_route(
                    rid,
                    sample_factory(),
                    expected_claims_key=expected_claims_key,
                    concurrency=concurrency,
                ))

        # Sort by composite score descending
        results.sort(key=lambda r: r.composite_score, reverse=True)

        return RouteEvalReport(
            task=self.task.name,
            samples=results[0].total_samples if results else 0,
            evaluated_at=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            routes=results,
        )

    evaluate_routes = evaluate_all
