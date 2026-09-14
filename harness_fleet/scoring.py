"""Intelligent route scoring engine integrating historical receipts and continuous eval data.

Verifiability (transport/schema/grounding) drives routing. Subjective claim
scores (0-100) must NOT drive routing: pin a single judge route for ranked
deliverables (two-phase funnel: fleet for recall, judge for ranking) and apply
per-route bias correction at export time. See export.adjust_claim_score and
store.update_route_claim_bias / get_route_claim_bias.
"""
from __future__ import annotations

import hashlib
import time
from typing import Any

from .models import RoutePolicy
from .store import HarnessStore

EXPLORATION_PRIOR = 0.55
SHRINKAGE_K = 10.0
W_GROUNDING = 0.30
W_MALFORMED = 0.25
W_RATE_LIMIT = 0.20
W_LATENCY = 0.15


def _base_score_from_stat(stat: dict[str, Any] | None) -> float | None:
    """Additive penalty score in [0.01, 1.0]. None if no data."""
    if not stat or stat.get("total", 0) <= 1e-9:
        return None
    total = float(stat["total"])
    completed = float(stat.get("completed", 0.0))
    malformed = float(stat.get("malformed", 0.0))
    schema_violations = float(stat.get("schema_violations", 0.0))
    grounding_failures = float(stat.get("grounding_failures", 0.0))
    rate_limits = float(stat.get("rate_limits", 0.0))
    avg_duration = float(stat.get("avg_duration", 0.0))

    # Laplace-smoothed verified rate
    verified_rate = (completed + 1.0) / (total + 2.0)
    grounding_rate = grounding_failures / total
    malformed_rate = (malformed + schema_violations) / total
    rl_ratio = rate_limits / total
    latency_norm = min(30.0, max(0.0, avg_duration)) / 30.0

    base = verified_rate - (
        W_GROUNDING * grounding_rate
        + W_MALFORMED * malformed_rate
        + W_RATE_LIMIT * rl_ratio
        + W_LATENCY * latency_norm
    )
    return max(0.01, min(1.0, base))


class RouteScorer:
    def __init__(self, store: HarnessStore):
        self.store = store

    def score_routes(
        self,
        routes: list[dict[str, Any]],
        task_name: str | None = None,
        half_life_hours: float = 72.0,
        shrinkage_k: float = SHRINKAGE_K,
    ) -> dict[str, float]:
        """Compute rolling composite quality score for each route in [0.0, 1.0].

        Uses time-decayed history (half_life_hours), additive penalties (no
        multiplicative collapse), and hierarchical shrinkage of task-specific
        stats toward global stats: w=n_task/(n_task+k).
        """
        if task_name:
            # Single history scan for both sides (see store.get_route_history_stats_pair).
            if hasattr(self.store, "get_route_history_stats_pair"):
                task_stats, global_stats = self.store.get_route_history_stats_pair(
                    task_name, half_life_hours=half_life_hours
                )
            else:
                task_stats = self.store.get_route_history_stats(task_name, half_life_hours=half_life_hours)
                global_stats = self.store.get_route_history_stats(None, half_life_hours=half_life_hours)
        else:
            task_stats = {}
            global_stats = self.store.get_route_history_stats(None, half_life_hours=half_life_hours)
        evals = self.store.get_route_evals(task_name)
        active_cooldowns = self.store.get_active_cooldowns()

        # Map latest eval composite_score per route, ordered explicitly by
        # created_at so the pick does not depend on store row order.
        latest_evals: dict[str, float] = {}
        latest_seen: dict[str, str] = {}
        for ev in evals:
            rid = ev["route_id"]
            seen_at = str(ev.get("created_at") or "")
            if rid not in latest_evals or seen_at >= latest_seen[rid]:
                latest_evals[rid] = float(ev.get("composite_score", 0.5))
                latest_seen[rid] = seen_at

        scores: dict[str, float] = {}
        now = time.time()

        for r in routes:
            rid = r["id"]

            if rid in active_cooldowns and active_cooldowns[rid] > now:
                scores[rid] = 0.0
                continue

            if task_name:
                task_stat = task_stats.get(rid)
                global_stat = global_stats.get(rid)
                base_task = _base_score_from_stat(task_stat)
                base_global = _base_score_from_stat(global_stat)
                if base_task is None and base_global is None:
                    base_score = EXPLORATION_PRIOR
                elif base_task is None:
                    base_score = base_global  # type: ignore[assignment]
                elif base_global is None:
                    base_score = base_task
                else:
                    n_task = float(task_stat.get("total", 0.0))  # type: ignore[union-attr]
                    if shrinkage_k > 0 and n_task >= 0:
                        w = n_task / (n_task + shrinkage_k)
                    elif n_task <= 0:
                        w = 0.0
                    else:
                        w = 1.0
                    w = max(0.0, min(1.0, w))
                    base_score = w * base_task + (1.0 - w) * base_global
            else:
                base = _base_score_from_stat(global_stats.get(rid))
                base_score = base if base is not None else EXPLORATION_PRIOR

            # Combine with benchmark evaluation score if available
            if rid in latest_evals:
                final_score = 0.6 * latest_evals[rid] + 0.4 * base_score
            else:
                final_score = base_score

            scores[rid] = round(max(0.01, min(1.0, final_score)), 4)

        return scores


def filter_and_rank_routes(
    routes: list[dict[str, Any]],
    store: HarnessStore,
    task_name: str | None = None,
    policy: RoutePolicy | None = None,
    seed: str = "",
    half_life_hours: float = 72.0,
    shrinkage_k: float = SHRINKAGE_K,
) -> list[str]:
    """Filter routes by policy and hard cooldown, then rank by intelligent composite score.

    Cost/latency SLOs belong in policy (allowed_routes / max_cost_*), not in the
    score product. For comparable claim scores, pin Phase-B scoring to a single
    judge via policy.allowed_routes=[judge] instead of round-robin.
    """
    ranked, _ = rank_with_scores(
        routes, store, task_name=task_name, policy=policy,
        half_life_hours=half_life_hours, shrinkage_k=shrinkage_k, seed=seed,
    )
    return ranked


def rank_with_scores(
    routes: list[dict[str, Any]],
    store: HarnessStore,
    task_name: str | None = None,
    policy: RoutePolicy | None = None,
    seed: str = "",
    half_life_hours: float = 72.0,
    shrinkage_k: float = SHRINKAGE_K,
) -> tuple[list[str], dict[str, float]]:
    """Filter, rank, and return (ranked_ids, score_map) from a single scoring pass.

    Prefer this over calling filter_and_rank_routes + score_routes separately:
    one history scan instead of two.
    """
    active_cooldowns = store.get_active_cooldowns()
    now = time.time()
    filtered: list[dict[str, Any]] = []

    for r in routes:
        rid = r["id"]
        prov = (r.get("provider") or rid.split("/", 1)[0]).lower()

        if rid in active_cooldowns and active_cooldowns[rid] > now:
            continue

        if policy:
            if getattr(policy, "free_only", False):
                declared_costs = (r.get("cost_per_1k_input"), r.get("cost_per_1k_output"))
                is_zero = (
                    not any(cost is not None and cost != 0.0 for cost in declared_costs)
                    and (
                        r.get("price_state") == "price_observed_zero"
                        or all(cost is not None and cost == 0.0 for cost in declared_costs)
                    )
                )
                if not is_zero:
                    continue
            allowed_t = policy.allowed_transports or policy.allowed_providers
            excluded_t = policy.excluded_transports or policy.excluded_providers
            if allowed_t and prov not in [p.lower() for p in allowed_t]:
                continue
            if excluded_t and prov in [p.lower() for p in excluded_t]:
                continue
            if policy.allowed_routes and rid not in policy.allowed_routes:
                continue
            if policy.excluded_routes and rid in policy.excluded_routes:
                continue
            if policy.max_cost_per_1k_input > 0 or policy.max_cost_per_1k_output > 0:
                cost_in = r.get("cost_per_1k_input")
                cost_out = r.get("cost_per_1k_output")
                # Observed-zero routes carry zero-price evidence stronger than
                # declared token costs, so ceilings treat them as cost 0.
                # Any other route with unknown (None) pricing fails closed:
                # undeclared costs cannot prove ceiling compliance.
                if r.get("price_state") != "price_observed_zero":
                    if policy.max_cost_per_1k_input > 0 and (
                        cost_in is None or cost_in > policy.max_cost_per_1k_input
                    ):
                        continue
                    if policy.max_cost_per_1k_output > 0 and (
                        cost_out is None or cost_out > policy.max_cost_per_1k_output
                    ):
                        continue

        filtered.append(r)

    if not filtered:
        return [], {}

    scorer = RouteScorer(store)
    score_map = scorer.score_routes(
        filtered, task_name=task_name, half_life_hours=half_life_hours, shrinkage_k=shrinkage_k
    )

    def sort_key(route_dict: dict[str, Any]) -> tuple[float, int]:
        rid = route_dict["id"]
        score = score_map.get(rid, 0.0)
        # Secondary tie breaker: always on. With a seed it hashes
        # (seed, route_id) for per-batch load distribution among peer routes;
        # without one it hashes the route id alone for a stable deterministic
        # order instead of input-order dependence.
        tie_input = f"{seed}:{rid}" if seed else rid
        tie_breaker = int(hashlib.sha256(tie_input.encode()).hexdigest()[:6], 16)
        return (score, tie_breaker)

    sorted_routes = sorted(filtered, key=sort_key, reverse=True)
    return [r["id"] for r in sorted_routes], score_map
