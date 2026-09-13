import concurrent.futures
import hashlib
import json
import time
import uuid
from collections.abc import Callable, Iterable, Iterator
from datetime import datetime, timezone
from itertools import chain
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple
from pydantic import ValidationError
from .catalog import RouteCatalog, is_observed_zero_price_route
from .export import export_clean_packet
from .grounding import normalize_grounding
from .models import CandidateModelOutput, InputItem, PackedBatch, ProviderReceipt, RoutePolicy, TaskSpec
from .packer import iter_packed_batches
from .providers.base import clean_llm_json
from .providers.registry import ProviderRegistry
from .sessions import SessionPool, WorkerSession
from .store import BulkLanesStore, STREAMING_INPUT_DIGEST


class _InputManifest:
    """Incrementally reproduce the canonical list digest without retaining items."""

    def __init__(self) -> None:
        self._hasher = hashlib.sha256()
        self._hasher.update(b"[")
        self.count = 0
        self._finished = False

    def add(self, raw_item: InputItem | dict[str, Any]) -> None:
        if self._finished:
            raise RuntimeError("input manifest is already finalized")
        if not isinstance(raw_item, InputItem):
            InputItem.model_validate(raw_item)
        payload = raw_item.model_dump(mode="json", by_alias=True) if hasattr(raw_item, "model_dump") else raw_item
        if self.count:
            self._hasher.update(b",")
        self._hasher.update(
            json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        )
        self.count += 1

    @property
    def digest(self) -> str:
        if not self._finished:
            self._hasher.update(b"]")
            self._finished = True
        return self._hasher.hexdigest()


def _manifest_stream(
    records: Iterable[InputItem | dict[str, Any]],
    manifest: _InputManifest,
) -> Iterator[InputItem | dict[str, Any]]:
    for raw_item in records:
        manifest.add(raw_item)
        yield raw_item

class Engine:
    def __init__(
        self,
        task: TaskSpec,
        catalog: Optional[RouteCatalog] = None,
        store: BulkLanesStore | None = None,
        max_attempts_per_batch: int = 3,
        policy: Optional[RoutePolicy] = None,
        registry: Optional[ProviderRegistry] = None,
        session_stickiness_tolerance: float = 0.10,
        profile: Any | None = None,
    ):
        self.task = task
        self.store = store or (catalog.store if catalog else BulkLanesStore())
        self.catalog = catalog or RouteCatalog(db_path=self.store.path)
        self.max_attempts_per_batch = max_attempts_per_batch
        self.policy = policy
        self.registry = registry or ProviderRegistry()
        # Session route is pinned first only while within tolerance of the best
        # score. Beyond that the ranked ladder wins and the session migrates to
        # the route that actually verifies (see execute_batch success path).
        self.session_stickiness_tolerance = max(0.0, float(session_stickiness_tolerance))
        self.profile = profile

    @property
    def opencode_prov(self):
        return self.registry.get("opencode")

    @opencode_prov.setter
    def opencode_prov(self, val):
        self.registry.register("opencode", val)

    @property
    def openrouter_prov(self):
        return self.registry.get("openrouter")

    @openrouter_prov.setter
    def openrouter_prov(self, val):
        self.registry.register("openrouter", val)

    def execute_batch(
        self,
        batch: Dict[str, Any],
        session: Optional[WorkerSession] = None,
        route_offset: int = 0,
        route_attempt_limit: int | None = None,
        run_id: Optional[str] = None,
    ) -> Tuple[bool, Optional[List[dict]], dict, Optional[str]]:
        """Executes a single multi-item batch within a worker session."""
        batch = PackedBatch.model_validate(batch).model_dump(mode="json")
        batch_id = batch["batch_id"]
        items = batch["items"]
        session_id = session.session_id if session else None
        
        # Build prompt payload
        simplified_items = []
        for itm in items:
            simplified_items.append({
                "item_id": itm["item_id"],
                "title": itm.get("title", ""),
                "sections": [
                    {
                        "slice_id": s["slice_id"],
                        "start": s["start"],
                        "end": s["end"],
                        "text": s["text"],
                    }
                    for s in itm.get("slices", [])
                ]
            })

        user_content = self.task.render_prompt(simplified_items)
        if self.profile is not None and hasattr(self.profile, "to_prompt_context"):
            user_content = (
                f"{self.profile.to_prompt_context()}\n\n"
                "Apply this profile as the qualification context. Follow the task's output schema and evidence rules.\n\n"
                f"{user_content}"
            )

        # Get route ladder with intelligent ranking and active policy filtering.
        # Session affinity is kept only while the session route scores within
        # tolerance of the best route; otherwise the ranked ladder wins and the
        # session migrates to whichever route actually verifies (success path).
        available_routes, route_scores = self.catalog.get_ladder_with_scores(
            task_seed=str(items[0]["item_id"]) if items else "",
            free_only=True,
            task_name=self.task.name,
            policy=self.policy,
        )
        if session and session.route_id in available_routes and not self.catalog.is_cooled_down(session.route_id):
            best = max((route_scores.get(r, 0.0) for r in available_routes), default=0.0)
            sess_score = route_scores.get(session.route_id, 0.0)
            if best - sess_score <= self.session_stickiness_tolerance:
                ladder = [session.route_id] + [r for r in available_routes if r != session.route_id]
            else:
                ladder = list(available_routes)
        else:
            ladder = list(available_routes)

        if ladder and route_offset:
            start = route_offset % len(ladder)
            ladder = ladder[start:] + ladder[:start]

        if not ladder:
            earliest_retry = self.catalog.get_earliest_cooldown_retry()
            if earliest_retry > 0:
                return False, None, {}, f"All matching routes are in active cooldown (earliest retry in {earliest_retry:.1f}s)."
            return False, None, {}, "No enabled route has observed zero pricing or matches active policy."

        last_err = "No attempts made"
        last_receipt = {}
        routes_by_id = {r["id"]: r for r in self.catalog.data.get("routes", [])}
        batch_id = batch.get("batch_id", "b0")

        attempt_limit = route_attempt_limit or self.max_attempts_per_batch
        for route_id in ladder[:attempt_limit]:
            route_info = routes_by_id.get(route_id)
            provider_hint = route_info.get("provider") if route_info else None
            provider = self.registry.resolve(provider_hint, route_id)

            import inspect
            prompt_kwargs: dict[str, Any] = {"session_id": session_id}
            sig = inspect.signature(provider.run_prompt)
            if "policy" in sig.parameters:
                prompt_kwargs["policy"] = self.policy

            started_ts = time.time()
            ok, response_text, receipt = provider.run_prompt(
                route_id=route_id,
                prompt=user_content,
                system_prompt=self.task.instructions,
                **prompt_kwargs,
            )
            last_receipt = receipt

            attempt_record = {
                "attempt_id": f"{run_id or 'adhoc'}:{batch_id}:{route_id}:{uuid.uuid4().hex[:8]}",
                "run_id": run_id,
                "batch_id": batch_id,
                "lease_attempt_number": (route_offset + 1) if route_offset is not None else 1,
                "route_id": route_id,
                "provider": receipt.get("provider") or provider_hint or "unknown",
                "task_name": self.task.name,
                "started_at": datetime.now(timezone.utc).isoformat(),
                "duration_seconds": receipt.get("duration_seconds") or (time.time() - started_ts),
                "cost": receipt.get("cost"),
                "cost_status": receipt.get("cost_status"),
                "usage": receipt.get("usage"),
                "retry_after": receipt.get("retry_after"),
                "error_type": receipt.get("error_type"),
                "error_message": receipt.get("error"),
                "transport_status": "success",
                "parse_status": "skipped",
                "schema_status": "skipped",
                "grounding_status": "skipped",
                "outcome": "failed",
                "verified": False,
                "counts_against_budget": 1,
            }

            try:
                ProviderReceipt.model_validate(receipt)
            except ValidationError as exc:
                last_err = f"Invalid provider receipt from '{route_id}': {exc}"
                attempt_record.update({
                    "transport_status": "failed",
                    "outcome": "transport_failed",
                    "error_type": "invalid_receipt",
                    "error_message": last_err,
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                if session:
                    session.record_error(last_err)
                continue

            if not ok:
                last_err = receipt.get("error", "Unknown provider error")
                error_type = receipt.get("error_type")
                retry_after = receipt.get("retry_after")

                if error_type in ("rate_limit", "transient_http"):
                    # Temporarily cool down route without burning batch attempt budget (adaptive if retry_after is None)
                    self.catalog.set_cooldown(route_id, retry_after, reason=f"{error_type}: {last_err}")
                    attempt_record.update({
                        "transport_status": error_type,
                        "outcome": "rate_limited" if error_type == "rate_limit" else "transport_failed",
                        "counts_against_budget": 0,
                    })
                    if self.store:
                        self.store.record_inference_attempt(attempt_record)
                    if session:
                        dur_str = f"{retry_after}s" if retry_after is not None else "adaptive"
                        session.record_error(f"[{route_id}] Cooldown ({dur_str}) applied: {last_err}")
                    continue

                attempt_record.update({
                    "transport_status": "failed",
                    "outcome": "transport_failed",
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                if session:
                    session.record_error(f"[{route_id}] {last_err}")
                continue

            # Record cost to monitor zero-price guarantee & circuit breaker
            try:
                self.catalog.record_cost(route_id, receipt.get("cost"), policy=self.policy)
            except Exception as e:
                attempt_record.update({
                    "transport_status": "circuit_breaker",
                    "outcome": "transport_failed",
                    "error_message": str(e),
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                return False, None, receipt, str(e)

            # Parse JSON
            parsed = clean_llm_json(response_text)
            if not parsed or not isinstance(parsed, dict):
                last_err = f"Malformed JSON from route '{route_id}'"
                attempt_record.update({
                    "transport_status": "success",
                    "parse_status": "malformed_json",
                    "outcome": "parse_failed",
                    "error_message": last_err,
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                if session:
                    session.record_error(last_err)
                continue

            try:
                candidate_output = CandidateModelOutput.model_validate(parsed)
                for extracted_item in candidate_output.items:
                    self.task.validate_claims(extracted_item.claims)
            except (ValidationError, ValueError) as exc:
                last_err = f"Typed output validation failed for '{route_id}': {exc}"
                attempt_record.update({
                    "transport_status": "success",
                    "parse_status": "success",
                    "schema_status": "schema_violation",
                    "outcome": "schema_failed",
                    "error_message": last_err,
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                if session:
                    session.record_error(last_err)
                continue

            output_items, ground_err = normalize_grounding(
                extracted_items=candidate_output.items,
                raw_cards=items,
                min_quote_chars=self.task.min_quote_chars,
            )

            if output_items is None:
                last_err = f"Grounding verification failed: {ground_err}"
                attempt_record.update({
                    "transport_status": "success",
                    "parse_status": "success",
                    "schema_status": "success",
                    "grounding_status": "grounding_failed",
                    "outcome": "grounding_failed",
                    "error_message": last_err,
                })
                if self.store:
                    self.store.record_inference_attempt(attempt_record)
                if session:
                    session.record_error(last_err)
                continue

            # Fully verified inference attempt!
            attempt_record.update({
                "transport_status": "success",
                "parse_status": "success",
                "schema_status": "success",
                "grounding_status": "success",
                "outcome": "verified",
                "verified": True,
            })
            if self.store:
                self.store.record_inference_attempt(attempt_record)

            # Record session success; migrate affinity to the route that verified.
            if session:
                tokens = receipt.get("usage", {}).get("total_tokens", 0) if isinstance(receipt.get("usage"), dict) else 0
                cost = receipt.get("cost", 0.0) or 0.0
                session.record_batch_success(items_count=len(output_items), tokens=tokens, cost=cost)
                if session.route_id != route_id:
                    session.route_id = route_id
                    session.provider = (
                        (route_info.get("provider") if isinstance(route_info, dict) else None)
                        or receipt.get("provider")
                        or session.provider
                    )

            return True, [item.model_dump(mode="json") for item in output_items], receipt, None

        return False, None, last_receipt, last_err

    def run_campaign(
        self,
        raw_items: Iterable[InputItem | dict[str, Any]],
        run_id: str,
        input_path: str,
        concurrency: int = 4,
        max_attempts: int = 300,
        output_packet_path: Optional[Path] = None,
        policy: Optional[RoutePolicy] = None,
        profile_revision_id: str | None = None,
        profile: Any | None = None,
        raw_items_factory: Callable[[], Iterable[InputItem | dict[str, Any]]] | None = None,
    ) -> dict:
        """Stream input into the durable SQLite queue, then execute its batches."""
        if concurrency < 1:
            raise ValueError("concurrency must be greater than 0")
        if max_attempts < 1:
            raise ValueError("max_attempts must be greater than 0")
        if self.max_attempts_per_batch < 1:
            raise ValueError("max_attempts_per_batch must be greater than 0")
        if policy:
            self.policy = policy
        selected_profile = profile if profile is not None else self.profile
        if selected_profile is not None:
            self.profile = selected_profile
            if profile_revision_id is None and hasattr(selected_profile, "profile_kind"):
                profile_revision_id = self.store.save_profile(selected_profile)
        elif profile_revision_id is not None:
            self.profile = self.store.load_profile_revision(profile_revision_id)
            if self.profile is None:
                raise ValueError(f"profile revision does not exist: {profile_revision_id}")
        task_revision = self.store.register_task(self.task)
        output_path = (output_packet_path or Path("runs") / run_id / "clean_packet.json").expanduser().resolve()

        # File-backed callers provide a factory so the source can be streamed
        # once for its manifest and once for durable batch insertion.  Lists
        # and other re-iterable containers get the same bounded path.  A
        # one-shot iterator uses the streaming-run manifest in the store.
        source_factory = raw_items_factory
        one_shot: Iterator[InputItem | dict[str, Any]] | None = None
        if source_factory is None:
            candidate = iter(raw_items)
            if candidate is raw_items:
                one_shot = candidate
            else:
                source_factory = lambda: iter(raw_items)

        if source_factory is not None:
            first_manifest = _InputManifest()
            for raw_item in source_factory():
                first_manifest.add(raw_item)
            if first_manifest.count == 0:
                raise ValueError("input contains no items")
            with self.store.streaming_run(
                run_id=run_id,
                task_revision_id=task_revision,
                profile_revision_id=profile_revision_id,
                input_path=input_path,
                input_digest=first_manifest.digest,
                total_items=first_manifest.count,
                max_attempts=max_attempts,
                batch_size=self.task.batch_size,
                output_path=str(output_path),
                policy=self.policy,
            ) as writer:
                second_manifest = _InputManifest()
                position = 0
                for batch in iter_packed_batches(
                    _manifest_stream(source_factory(), second_manifest),
                    batch_size=self.task.batch_size,
                    max_slice_chars=self.task.max_slice_chars,
                ):
                    writer.enqueue_batch(batch, position, self.max_attempts_per_batch)
                    position += 1
                if (second_manifest.count, second_manifest.digest) != (first_manifest.count, first_manifest.digest):
                    raise ValueError("input changed while it was being streamed; no batches were committed")
        else:
            if one_shot is None:
                raise ValueError("raw_items must be iterable")
            try:
                first_item = next(one_shot)
            except StopIteration as exc:
                raise ValueError("input contains no items") from exc
            with self.store.streaming_run(
                run_id=run_id,
                task_revision_id=task_revision,
                profile_revision_id=profile_revision_id,
                input_path=input_path,
                input_digest=STREAMING_INPUT_DIGEST,
                total_items=0,
                max_attempts=max_attempts,
                batch_size=self.task.batch_size,
                output_path=str(output_path),
                policy=self.policy,
            ) as writer:
                manifest = _InputManifest()
                position = 0
                for batch in iter_packed_batches(
                    _manifest_stream(chain((first_item,), one_shot), manifest),
                    batch_size=self.task.batch_size,
                    max_slice_chars=self.task.max_slice_chars,
                ):
                    writer.enqueue_batch(batch, position, self.max_attempts_per_batch)
                    position += 1
                writer.finalize(manifest.digest, manifest.count)

        return self.resume_campaign(run_id, concurrency=concurrency, output_packet_path=output_path)

    def resume_campaign(
        self,
        run_id: str,
        concurrency: int = 4,
        output_packet_path: Optional[Path] = None,
    ) -> dict:
        """Resume pending SQLite queue work without reconstructing it from input files."""
        if concurrency < 1:
            raise ValueError("concurrency must be greater than 0")
        self.task = self.store.get_run_task(run_id)
        snapshot = self.store.run_snapshot(run_id)
        if snapshot.get("input_digest") == STREAMING_INPUT_DIGEST:
            raise RuntimeError(f"run {run_id} is still ingesting input; finish the original process before resuming")
        profile_revision_id = snapshot.get("profile_revision_id")
        self.profile = (
            self.store.load_profile_revision(profile_revision_id)
            if profile_revision_id
            else None
        )
        if profile_revision_id and self.profile is None:
            raise ValueError(f"profile revision for run {run_id} does not exist: {profile_revision_id}")
        # Reclaim any batches abandoned in 'leased' status from prior interrupted worker sessions
        self.store.reset_leased_batches(run_id)

        # A completed run can always be re-exported; it must not require a
        # currently available model route just to read already verified data.
        has_work = any(
            batch.get("status") in {"pending", "leased"}
            for batch in (snapshot.get("batches") or {}).values()
        )
        if not has_work:
            raw_out = snapshot.get("output_path")
            export_path = output_packet_path or (
                Path(raw_out) if raw_out else Path("runs") / run_id / "clean_packet.json"
            )
            return export_clean_packet(snapshot, export_path)

        if not self.policy and snapshot.get("policy"):
            stored_policy = RoutePolicy.model_validate(snapshot["policy"])
            # Run policy is retained as audit history, but a paid allowlist is
            # never an approval token for a later process/session. Keep only
            # currently free routes when resuming implicitly.
            if stored_policy.allowed_routes:
                route_map = {r["id"]: r for r in self.catalog.data.get("routes", [])}
                free_routes = [
                    route_id for route_id in stored_policy.allowed_routes
                    if route_id in route_map and is_observed_zero_price_route(route_map[route_id])
                ]
                if len(free_routes) != len(stored_policy.allowed_routes):
                    if not free_routes:
                        raise RuntimeError(
                            "This run includes a previously approved paid route; "
                            "resume it with an explicit --route approval."
                        )
                    stored_policy.allowed_routes = free_routes
                    stored_policy.free_only = True
            self.policy = stored_policy

        available_free_routes = self.catalog.get_ladder(
            task_seed=str(time.time()),
            free_only=True,
            task_name=self.task.name,
            policy=self.policy,
        )
        if not available_free_routes:
            raise RuntimeError("No enabled route has observed zero pricing or matches active policy.")
        session_pool = SessionPool(num_sessions=concurrency, routes=available_free_routes)
        sessions_list = session_pool.get_all_sessions()

        def session_worker(session: WorkerSession) -> None:
            while True:
                lease = self.store.lease_batch(run_id, session.session_id)
                if lease is None:
                    return
                batch = lease["batch"]
                ok, results, raw_receipt, error = self.execute_batch(
                    batch,
                    session=session,
                    route_offset=lease["attempt_number"] - 1,
                    route_attempt_limit=self.max_attempts_per_batch,
                    run_id=run_id,
                )
                try:
                    receipt = ProviderReceipt.model_validate(raw_receipt) if raw_receipt else None
                except ValidationError:
                    receipt = None
                if ok and results is not None and receipt is not None:
                    self.store.complete_batch(run_id, lease["attempt_id"], session.session_id, results, receipt)
                elif (receipt and receipt.error_type in ("rate_limit", "transient_http")) or "active cooldown" in (error or "").lower():
                    # Release lease back to pending without consuming attempt
                    earliest_retry = self.catalog.get_earliest_cooldown_retry()
                    sleep_time = min(5.0, max(0.5, earliest_retry)) if earliest_retry > 0 else 1.0
                    self.store.release_lease(
                        run_id,
                        lease["attempt_id"],
                        session.session_id,
                        reason=error or "Rate limit / transient error / route cooldown",
                    )
                    time.sleep(sleep_time)
                else:
                    self.store.fail_batch(
                        run_id,
                        lease["attempt_id"],
                        session.session_id,
                        error or "Unknown error",
                        receipt,
                    )

        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
            list(executor.map(session_worker, sessions_list))

        for s in sessions_list:
            s.finish()

        self.store.save_sessions(run_id, session_pool.to_dict())
        self.store.finalize_run(run_id)
        snapshot = self.store.run_snapshot(run_id)
        raw_out = snapshot.get("output_path")
        export_path = output_packet_path or (Path(raw_out) if raw_out else Path("runs") / run_id / "clean_packet.json")
        packet = export_clean_packet(snapshot, export_path)
        
        return packet
