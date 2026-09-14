"""Deterministic calibration fitting for scoring tasks.

Workers answer labeled samples; coordinate descent then fits checklist
points, source weights, and recency half-lives against expected scores.
The optimizer is fully deterministic (fixed order, fixed steps, no random
init), so the same observations always yield the same calibration.
Observation collection itself depends on worker behavior — the report
records the route so a fit can be interpreted, not blindly trusted.

Points are constrained to the probability simplex (non-negative, sum to
100) so the fixed tier bands keep their meaning; fitted points round to
ints with largest remainder. Train/holdout splits are stride-based, hence
reproducible.
"""
from __future__ import annotations

import hashlib
import math
from datetime import datetime, timezone
from typing import Any

from .grounding import normalize_grounding
from .models import CandidateModelOutput, InputItem, TaskSpec
from .providers.base import clean_llm_json

PARAM_KINDS = ("points", "weights", "halves")

# (initial step, minimum step, lower bound, upper bound) per kind.
# Points move additively; weights additively in [0, 1]; half-lives
# multiplicatively in [0.5 days, 20 years].
_STEP_CONFIG = {
    "points": {"step": 4.0, "min_step": 0.25, "lo": 0.0, "hi": 100.0, "mult": False},
    "weights": {"step": 0.2, "min_step": 0.01, "lo": 0.0, "hi": 1.0, "mult": False},
    "halves": {"step": 1.6, "min_step": 1.02, "lo": 0.5, "hi": 7300.0, "mult": True},
}
MAX_SWEEPS = 50
MIN_USABLE_SAMPLES = 2


def _project_simplex(values: list[float], total: float = 100.0) -> list[float]:
    """Project onto {x >= 0, sum x = total}; deterministic sort-based method."""
    n = len(values)
    if n == 0:
        return []
    ordered = sorted(values, reverse=True)
    cumsum = 0.0
    rho = 0
    for i, value in enumerate(ordered, start=1):
        cumsum += value
        if value + (total - cumsum) / i > 0:
            rho = i
    theta = (sum(ordered[:rho]) - total) / rho if rho else 0.0
    return [max(0.0, value - theta) for value in values]


def round_points_to_simplex(points: dict[str, float], total: int = 100) -> dict[str, int]:
    """Round fitted points to ints summing to total via largest remainder."""
    names = sorted(points)
    if not names:
        return {}
    floors = {name: math.floor(points[name]) for name in names}
    remainders = sorted(
        ((points[name] - floors[name], name) for name in names),
        key=lambda pair: (-pair[0], pair[1]),
    )
    leftover = total - sum(floors.values())
    result = dict(floors)
    for i in range(max(0, leftover)):
        result[remainders[i % len(remainders)][1]] += 1
    return result


def _mse(errors: list[float]) -> float:
    return sum(e * e for e in errors) / len(errors) if errors else 0.0


def _mae(errors: list[float]) -> float:
    return sum(abs(e) for e in errors) / len(errors) if errors else 0.0


def collect_observations(
    task: TaskSpec,
    samples: list[InputItem],
    expected_key: str,
    run_prompt: Any,
    route_id: str,
) -> tuple[list[dict[str, Any]], dict[str, int], str]:
    """Run one rater route over labeled samples; return observations and time.

    Each observation carries checklist answers, quote supports, source URI,
    capture time, and the expected score. Samples with non-numeric labels
    or unverifiable worker output are skipped with counted reasons. The
    returned scoring instant must feed fit_calibration so decay matches the
    grounded records exactly.
    """
    scored_at = datetime.now(timezone.utc).isoformat()
    observations: list[dict[str, Any]] = []
    skipped: dict[str, int] = {}
    for item in samples:
        expected = item.metadata.get(expected_key)
        try:
            expected_score = float(expected)  # type: ignore[arg-type]
        except (TypeError, ValueError):
            skipped["non_numeric_label"] = skipped.get("non_numeric_label", 0) + 1
            continue
        if not math.isfinite(expected_score):
            skipped["non_numeric_label"] = skipped.get("non_numeric_label", 0) + 1
            continue
        prompt = task.render_prompt([{
            "item_id": item.item_id,
            "title": item.title or "",
            "sections": [{
                "slice_id": "full",
                "start": 0,
                "end": len(item.text),
                "text": item.text,
            }],
        }])
        ok, response_text, _receipt = run_prompt(
            route_id=route_id, prompt=prompt,
            system_prompt=task.render_instructions(),
        )
        if not ok:
            skipped["transport_failed"] = skipped.get("transport_failed", 0) + 1
            continue
        parsed = clean_llm_json(response_text)
        if not parsed or not isinstance(parsed, dict):
            skipped["malformed_json"] = skipped.get("malformed_json", 0) + 1
            continue
        try:
            candidate = CandidateModelOutput.model_validate(parsed)
            if len(candidate.items) != 1:
                raise ValueError("expected exactly one item")
            extracted = candidate.items[0]
            strengths = task.support_strengths(extracted.quotes, item.source_uri)
            task.validate_claims(extracted.claims, quotes=extracted.quotes, strengths=strengths)
        except ValueError:
            skipped["schema_failed"] = skipped.get("schema_failed", 0) + 1
            continue
        captured = item.metadata.get("captured_at")
        digest = hashlib.sha256(item.text.encode("utf-8")).hexdigest()
        output_items, ground_err = normalize_grounding(
            extracted_items=[extracted],
            raw_cards=[{
                "item_id": item.item_id,
                "source_digest": digest,
                "content_type": item.content_type,
                "metadata": dict(item.metadata),
                "slices": [{
                    "slice_id": "full", "start": 0,
                    "end": len(item.text), "text": item.text,
                }],
            }],
            min_quote_chars=task.min_quote_chars,
            evidence_terms=task.evidence_terms,
            candidate_top_n=task.candidate_top_n,
            scored_at=scored_at,
        )
        if output_items is None:
            _ = ground_err
            skipped["grounding_failed"] = skipped.get("grounding_failed", 0) + 1
            continue
        observations.append({
            "item_id": item.item_id,
            "answers": dict(extracted.claims.get("checklist") or {}),  # type: ignore[arg-type]
            "quotes": [quote.model_dump(mode="json") for quote in extracted.quotes],
            "source_uri": item.source_uri,
            "captured_at": captured if isinstance(captured, str) else None,
            "expected": expected_score,
        })
    return observations, skipped, scored_at


def _evaluate(
    task: TaskSpec,
    points: dict[str, float],
    weights: dict[str, float],
    default_weight: float,
    halves: dict[str, float],
    observations: list[dict[str, Any]],
    scored_at: str,
) -> list[float]:
    """Derived-score errors for observations under candidate calibration."""
    trial = task.model_copy(update={
        "checklist": dict(points),
        "source_weights": dict(weights),
        "default_source_weight": default_weight,
        "recency_half_lives": dict(halves),
    })
    errors: list[float] = []
    for obs in observations:
        strengths = trial.support_strengths(
            obs["quotes"], obs["source_uri"], obs["captured_at"], scored_at
        )
        try:
            score = trial.derive_checklist_score(obs["answers"], strengths)
        except ValueError:
            score = 0
        errors.append(score - obs["expected"])
    return errors


def _param_order(
    points: dict[str, float],
    weights: dict[str, float],
    halves: dict[str, float],
) -> list[tuple[str, str]]:
    return (
        [("points", name) for name in sorted(points)]
        + [("weights", name) for name in sorted(weights)]
        + [("halves", name) for name in sorted(halves)]
    )


def fit_calibration(
    task: TaskSpec,
    observations: list[dict[str, Any]],
    scored_at: str,
    kinds: tuple[str, ...] = PARAM_KINDS,
    max_sweeps: int = MAX_SWEEPS,
) -> dict[str, Any]:
    """Coordinate descent on MSE; points projected to the simplex per sweep.

    Starts from the task's current calibration, so a fit refines rather
    than reinvents. Returns fitted tables plus train/holdout error metrics.
    """
    if task.checklist is None:
        raise ValueError("calibration needs a task with a checklist")
    if len(observations) < MIN_USABLE_SAMPLES:
        raise ValueError(f"need at least {MIN_USABLE_SAMPLES} usable observations")
    for kind in kinds:
        if kind not in PARAM_KINDS:
            raise ValueError(f"unknown param kind '{kind}'; choose from {PARAM_KINDS}")

    points = {name: float(value) for name, value in task.checklist.items()}
    weights = {name: float(value) for name, value in task.source_weights.items()}
    halves = {name: float(value) for name, value in task.recency_half_lives.items()}
    default_weight = float(task.default_source_weight)
    fit_weights = "weights" in kinds and bool(weights)
    fit_default = "weights" in kinds
    fit_halves = "halves" in kinds and bool(halves)
    fit_points = "points" in kinds

    stride = 5
    train = [obs for index, obs in enumerate(observations) if index % stride != 0]
    held = [obs for index, obs in enumerate(observations) if index % stride == 0]
    if not train:
        train, held = observations, []

    def _bounds(kind: str) -> dict[str, Any]:
        if kind == "default":
            return {"step": steps["weights"], "min_step": 0.01, "lo": 0.0, "hi": 1.0, "mult": False}
        return _STEP_CONFIG[kind]

    def _read(kind: str, name: str) -> float:
        if kind == "points":
            return points[name]
        if kind == "weights":
            return weights[name]
        if kind == "halves":
            return halves[name]
        return default_weight

    Trial = tuple[dict[str, float], dict[str, float], float, dict[str, float]]  # noqa: N806

    def _trial(kind: str, name: str, value: float) -> Trial:
        if kind == "points":
            trial_points = dict(points)
            trial_points[name] = value
            return trial_points, weights, default_weight, halves
        if kind == "weights":
            trial_weights = dict(weights)
            trial_weights[name] = value
            return points, trial_weights, default_weight, halves
        if kind == "halves":
            trial_halves = dict(halves)
            trial_halves[name] = value
            return points, weights, default_weight, trial_halves
        return points, weights, value, halves

    def _commit(kind: str, name: str, value: float) -> None:
        nonlocal points, weights, default_weight, halves
        if kind == "points":
            points[name] = value
        elif kind == "weights":
            weights[name] = value
        elif kind == "halves":
            halves[name] = value
        else:
            default_weight = value

    def _sweep() -> bool:
        """One pass over every fitted param in fixed order; True if MSE improved."""
        nonlocal best, points, weights, default_weight, halves
        improved = False
        order = _param_order(
            points if fit_points else {},
            weights if fit_weights else {},
            halves if fit_halves else {},
        )
        if fit_default:
            order.append(("default", "default_source_weight"))
        for kind, name in order:
            cfg = _bounds(kind)
            current = _read(kind, name)
            step = steps["weights"] if kind == "default" else steps[kind]
            for direction in (1.0, -1.0):
                if cfg["mult"]:
                    trial_value = current * (step**direction)
                else:
                    trial_value = current + direction * step
                trial_value = max(cfg["lo"], min(cfg["hi"], trial_value))
                if trial_value == current:
                    continue
                trial = _trial(kind, name, trial_value)
                trial_mse = _mse(_evaluate(task, *trial, train, scored_at))
                if trial_mse < best - 1e-9:
                    best = trial_mse
                    improved = True
                    _commit(kind, name, trial_value)
                    current = trial_value
        return improved

    def _shrink() -> bool:
        """Halve every step above its floor (sqrt for multiplicative); True if any moved."""
        shrinkable = False
        for kind in PARAM_KINDS:
            cfg = _STEP_CONFIG[kind]
            if steps[kind] <= cfg["min_step"]:
                continue
            if cfg["mult"]:
                steps[kind] = max(cfg["min_step"], math.sqrt(steps[kind]))
            else:
                steps[kind] = max(cfg["min_step"], steps[kind] / 2.0)
            shrinkable = True
        return shrinkable

    def _project_points() -> None:
        """Renormalize fitted points onto the simplex after each sweep."""
        nonlocal best, points
        if not (fit_points and points):
            return
        order_names = sorted(points)
        projected = _project_simplex([points[name] for name in order_names])
        points = dict(zip(order_names, projected, strict=False))
        projected_mse = _mse(_evaluate(
            task, points, weights, default_weight, halves, train, scored_at
        ))
        best = min(best, projected_mse)

    steps = {kind: _STEP_CONFIG[kind]["step"] for kind in PARAM_KINDS}
    best = _mse(_evaluate(task, points, weights, default_weight, halves, train, scored_at))
    sweeps = 0
    while sweeps < max_sweeps:
        sweeps += 1
        _project_points()
        if not _sweep() and not _shrink():
            break
    _project_points()

    rounded_points = round_points_to_simplex(points)
    rounded_errors = _evaluate(
        task,
        {name: float(value) for name, value in rounded_points.items()},
        weights, default_weight, halves, train, scored_at,
    )
    held_errors = (
        _evaluate(task, points, weights, default_weight, halves, held, scored_at)
        if held else []
    )
    baseline_errors = _evaluate(
        task,
        {name: float(value) for name, value in task.checklist.items()},
        {name: float(value) for name, value in task.source_weights.items()},
        float(task.default_source_weight),
        {name: float(value) for name, value in task.recency_half_lives.items()},
        train, scored_at,
    )
    return {
        "sweeps": sweeps,
        "n_params": len(_param_order(
            points if fit_points else {},
            weights if fit_weights else {},
            halves if fit_halves else {},
        )) + (1 if fit_default else 0),
        "n_train": len(train),
        "n_holdout": len(held),
        "baseline": {"mse": _mse(baseline_errors), "mae": _mae(baseline_errors)},
        "fitted_train": {"mse": _mse(rounded_errors), "mae": _mae(rounded_errors)},
        "fitted_holdout": {"mse": _mse(held_errors), "mae": _mae(held_errors)} if held else None,
        "points": rounded_points,
        "points_float": {name: round(value, 4) for name, value in points.items()},
        "weights": {name: round(value, 4) for name, value in weights.items()},
        "default_source_weight": round(default_weight, 4),
        "halves": {name: round(value, 4) for name, value in halves.items()},
    }
