"""Declarative DAG workflows over the existing executor.

Every workflow is a directed acyclic graph of typed nodes. Edges carry ID
sets and run references in-process — never lossy CSV round-trips — so
multi-stage funnels keep full drill-through (offsets, digests) at every hop.
Nodes reuse the current primitives (Engine campaigns, export filters), and
each ``run``/``rescore`` node is one SQLite run, so per-node resume works
unchanged. Multi-step flows (rescore rounds, calibration fitting) are node
kinds here, not imperative scripts: the CLI builds specs and runs them.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any, Literal

from pydantic import Field, model_validator

from .calibrate import PARAM_KINDS, collect_observations, fit_calibration
from .catalog import RouteCatalog
from .engine import Engine
from .export import (
    _filter_and_sort_records,
    export_clean_packet,
    verified_records_from_snapshot,
)
from .input_data import load_input_items
from .models import CalibrationReport, ClaimFilter, ClosedModel, RoutePolicy, SortSpec
from .profile import IdealCompanyProfile
from .providers.registry import ProviderRegistry, ProviderResolutionError
from .store import HarnessStore
from .task import load_task_spec


class DagError(ValueError):
    pass


class RunNode(ClosedModel):
    kind: Literal["run"] = "run"
    id: str
    task: str | None = None
    task_from: str | None = None
    input: str
    ids_from: list[str] = []
    ids_mode: Literal["union", "intersection"] = "intersection"
    sessions: int = 4
    max_attempts: int = 300
    policy: RoutePolicy | None = None
    id_column: str | None = None
    text_column: str | None = None
    title_column: str | None = None
    uri_column: str | None = None

    @model_validator(mode="after")
    def check_task_source(self) -> RunNode:
        if (self.task is None) == (self.task_from is None):
            raise DagError("run node needs exactly one of 'task' or 'task_from'")
        return self


class RescoreNode(ClosedModel):
    """A rescore round: fresh evidence as one SQLite run linked to a parent run.

    The parent is either an upstream node (``from_run``) or a literal
    existing run id (``parent_run``, the CLI spelling) — exactly one.
    Without ``task`` the parent run's task is reused.
    """
    kind: Literal["rescore"] = "rescore"
    id: str
    from_run: str | None = None
    parent_run: str | None = None
    task: str | None = None
    input: str
    run_id: str | None = None
    output: str | None = None
    sessions: int = 4
    max_attempts: int = 300
    policy: RoutePolicy | None = None
    id_column: str | None = None
    text_column: str | None = None
    title_column: str | None = None
    uri_column: str | None = None
    profile: str | None = None
    use_active_profile: bool = False

    @model_validator(mode="after")
    def check_parent_source(self) -> RescoreNode:
        if (self.from_run is None) == (self.parent_run is None):
            raise DagError("rescore node needs exactly one of 'from_run' or 'parent_run'")
        return self


class CalibrateNode(ClosedModel):
    """Fit scoring calibration against labeled samples with one rater route.

    On ``apply`` the recalibrated task revision is registered and recorded,
    so a downstream run node can consume it via ``task_from``.
    """
    kind: Literal["calibrate"] = "calibrate"
    id: str
    task: str
    input: str
    expected: str = "score"
    route: str
    params: list[str] = Field(default_factory=lambda: list(PARAM_KINDS))
    max_sweeps: int = Field(default=50, ge=1)
    apply: bool = False
    id_column: str | None = None
    text_column: str | None = None
    title_column: str | None = None
    uri_column: str | None = None

    @model_validator(mode="after")
    def check_params(self) -> CalibrateNode:
        unknown = [name for name in self.params if name not in PARAM_KINDS]
        if unknown:
            raise DagError(f"calibrate node params {unknown} are not in {list(PARAM_KINDS)}")
        if not self.params:
            raise DagError("calibrate node needs at least one param to fit")
        return self


class FilterNode(ClosedModel):
    kind: Literal["filter"] = "filter"
    id: str
    from_run: str
    filter: ClaimFilter | None = None
    sort: SortSpec | None = None
    top: int | None = None


class ExportNode(ClosedModel):
    kind: Literal["export"] = "export"
    id: str
    from_run: str
    format: Literal["json", "csv", "jsonl"] = "json"
    output: str | None = None
    sort: SortSpec | None = None
    top: int | None = None
    rank: bool = False
    filter: ClaimFilter | None = None


DagNode = RunNode | RescoreNode | CalibrateNode | FilterNode | ExportNode


class DagSpec(ClosedModel):
    name: str
    nodes: list[RunNode | RescoreNode | CalibrateNode | FilterNode | ExportNode]

    @model_validator(mode="after")
    def check_graph(self) -> DagSpec:
        # Fail fast at parse time: duplicate ids, unknown or wrong-kind
        # references, self-edges, and cycles never reach the executor.
        self.topo_order()
        return self

    def topo_order(self) -> list[str]:
        """Topological node ids (Kahn's algorithm, spec order breaks ties).

        Raises DagError on duplicate ids, unknown references, wrong-kind
        references, self-edges, and cycles.
        """
        by_id: dict[str, DagNode] = {}
        for node in self.nodes:
            if node.id in by_id:
                raise DagError(f"duplicate node id '{node.id}'")
            by_id[node.id] = node

        deps: dict[str, set[str]] = {}
        for node in self.nodes:
            if isinstance(node, RunNode):
                refs = list(node.ids_from)
                for ref in refs:
                    target = by_id.get(ref)
                    if target is None:
                        raise DagError(f"node '{node.id}' references unknown node '{ref}'")
                    if not isinstance(target, FilterNode):
                        raise DagError(f"node '{node.id}' ids_from '{ref}' is not a filter node")
                    if ref == node.id:
                        raise DagError(f"node '{node.id}' references itself")
                task_refs = [node.task_from] if node.task_from else []
                for ref in task_refs:
                    target = by_id.get(ref)
                    if target is None:
                        raise DagError(f"node '{node.id}' references unknown node '{ref}'")
                    if not isinstance(target, CalibrateNode):
                        raise DagError(f"node '{node.id}' task_from '{ref}' is not a calibrate node")
                    if ref == node.id:
                        raise DagError(f"node '{node.id}' references itself")
                deps[node.id] = set(refs) | set(task_refs)
            elif isinstance(node, RescoreNode):
                if node.from_run is None:
                    deps[node.id] = set()
                    continue
                ref = node.from_run
                target = by_id.get(ref)
                if target is None:
                    raise DagError(f"node '{node.id}' references unknown node '{ref}'")
                if not isinstance(target, (RunNode, RescoreNode)):
                    raise DagError(f"node '{node.id}' from_run '{ref}' is not a run or rescore node")
                if ref == node.id:
                    raise DagError(f"node '{node.id}' references itself")
                deps[node.id] = {ref}
            elif isinstance(node, CalibrateNode):
                deps[node.id] = set()
            else:
                ref = node.from_run
                target = by_id.get(ref)
                if target is None:
                    raise DagError(f"node '{node.id}' references unknown node '{ref}'")
                if not isinstance(target, (RunNode, RescoreNode)):
                    raise DagError(f"node '{node.id}' from_run '{ref}' is not a run or rescore node")
                if ref == node.id:
                    raise DagError(f"node '{node.id}' references itself")
                deps[node.id] = {ref}

        order: list[str] = []
        resolved: set[str] = set()
        remaining = list(by_id)
        while remaining:
            ready = [nid for nid in remaining if deps[nid] <= resolved]
            if not ready:
                raise DagError(f"dependency cycle among nodes: {sorted(remaining)}")
            for nid in ready:
                order.append(nid)
                resolved.add(nid)
                remaining.remove(nid)
        return order


def spec_digest(spec: DagSpec) -> str:
    payload = spec.model_dump(mode="json")
    return hashlib.sha256(
        json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()


def _resolve_dag_task(reference: str, store: HarnessStore, workspace_root: Path):
    path = (workspace_root / reference).expanduser()
    if path.is_file():
        spec = load_task_spec(path)
        store.register_task(spec)
        return spec
    return store.get_task(reference)


def _resolve_node_profile(node: RescoreNode, store: HarnessStore, root: Path):
    """Profile selection for rescore nodes, mirroring the run command."""
    if node.profile:
        path = Path(node.profile).expanduser()
        profile = IdealCompanyProfile.load(path if path.is_absolute() else root / path)
        return profile, store.save_profile(profile)
    if node.use_active_profile:
        revision = store.active_profile_revision_id("ideal_company")
        if revision:
            return store.load_profile("ideal_company"), revision
    return None, None


def _execute_rescore_node(
    node: RescoreNode,
    run_id: str,
    state: dict[str, Any],
    store: HarnessStore,
    root: Path,
    dag_id: str,
) -> None:
    parent = f"{dag_id}-{node.from_run}" if node.from_run else node.parent_run
    assert parent is not None
    try:
        store.run_snapshot(parent)
    except KeyError as exc:
        raise DagError(f"rescore node '{node.id}' parent run '{parent}' does not exist") from exc
    task = _resolve_dag_task(node.task, store, root) if node.task else store.get_run_task(parent)
    profile, profile_revision_id = _resolve_node_profile(node, store, root)
    input_path = (root / node.input).expanduser()
    items = load_input_items(
        input_path,
        id_column=node.id_column,
        text_column=node.text_column,
        title_column=node.title_column,
        uri_column=node.uri_column,
    )
    policy = node.policy
    output_path = (root / node.output).expanduser() if node.output else root / "runs" / run_id / "clean_packet.json"
    packet = Engine(task=task, store=store, policy=policy).run_campaign(
        raw_items=items,
        run_id=run_id,
        input_path=str(input_path),
        concurrency=node.sessions,
        max_attempts=node.max_attempts,
        output_packet_path=output_path,
        policy=policy,
        profile_revision_id=profile_revision_id,
        profile=profile,
        parent_run_id=parent,
    )
    state["nodes"][node.id] = {
        "kind": "rescore",
        "run_id": run_id,
        "parent_run": parent,
        "verified": packet.get("total_verified_records", 0),
        "input_digest": packet.get("input_digest"),
        "output": str(output_path),
    }


def _execute_calibrate_node(
    node: CalibrateNode,
    state: dict[str, Any],
    store: HarnessStore,
    root: Path,
    dag_dir: Path,
) -> None:
    from .models import TaskSpec

    task = _resolve_dag_task(node.task, store, root)
    if task.checklist is None:
        raise DagError(f"calibrate node '{node.id}' task '{task.name}' has no checklist to fit")
    catalog = RouteCatalog(db_path=store.path)
    routes_by_id = {item["id"]: item for item in catalog.data.get("routes", [])}
    if node.route not in routes_by_id:
        raise DagError(f"calibrate node '{node.id}' unknown route '{node.route}'")
    provider_hint = routes_by_id[node.route].get("provider")
    try:
        provider = ProviderRegistry().resolve(provider_hint)
    except ProviderResolutionError as exc:
        raise DagError(f"calibrate node '{node.id}' {exc}") from exc
    input_path = (root / node.input).expanduser()
    samples = load_input_items(
        input_path,
        id_column=node.id_column,
        text_column=node.text_column,
        title_column=node.title_column,
        uri_column=node.uri_column,
    )
    if not samples:
        raise DagError(f"calibrate node '{node.id}' input '{node.input}' contains no samples")
    observations, skipped, scored_at = collect_observations(
        task, samples, node.expected, provider.run_prompt, node.route
    )
    fit = fit_calibration(
        task, observations, scored_at,
        kinds=tuple(node.params), max_sweeps=node.max_sweeps,
    )
    new_revision = None
    if node.apply:
        recalibrated = TaskSpec.model_validate(task.model_copy(update={
            "checklist": fit["points"],
            "source_weights": fit["weights"],
            "default_source_weight": fit["default_source_weight"],
            "recency_half_lives": fit["halves"],
        }).model_dump(mode="json"))
        new_revision = store.register_task(recalibrated)
    report = CalibrationReport(
        task=task.name, route=node.route, scored_at=scored_at, params=sorted(node.params),
        sweeps=fit["sweeps"], n_train=fit["n_train"], n_holdout=fit["n_holdout"],
        n_skipped=sum(skipped.values()), skipped=skipped,
        baseline=fit["baseline"], fitted_train=fit["fitted_train"],
        fitted_holdout=fit["fitted_holdout"], points=fit["points"],
        points_float=fit["points_float"], weights=fit["weights"],
        default_source_weight=fit["default_source_weight"], halves=fit["halves"],
        applied=node.apply, new_revision=new_revision,
    ).model_dump(mode="json")
    node_dir = dag_dir / node.id
    node_dir.mkdir(parents=True, exist_ok=True)
    (node_dir / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    state["nodes"][node.id] = {
        "kind": "calibrate",
        "task": task.name,
        "report": report,
        "revision": new_revision,
    }


def _combine_ids(sets: list[set[str]], mode: str) -> set[str] | None:
    if not sets:
        return None
    if mode == "union":
        combined: set[str] = set()
        for s in sets:
            combined |= s
        return combined
    combined = set(sets[0])
    for s in sets[1:]:
        combined &= s
    return combined


def _run_complete(store: HarnessStore, run_id: str) -> bool:
    try:
        snapshot = store.run_snapshot(run_id)
    except Exception:
        return False
    batches = (snapshot.get("batches") or {}).values()
    return bool(list(batches)) and all(
        b.get("status") in ("verified", "failed") for b in batches
    )


def run_dag(
    spec: DagSpec,
    store: HarnessStore,
    workspace_root: str | Path = ".",
    dag_id: str | None = None,
    resume: bool = True,
) -> dict[str, Any]:
    """Execute a DAG spec topologically; return a lineage summary.

    Node run ids are deterministic (``{dag_id}-{node_id}``), so re-running
    resumes: completed ``run`` nodes are reused from SQLite, while
    ``filter``/``export`` nodes re-execute (cheap and deterministic).
    Lineage (spec digest, per-node run ids, counts, digests, artifacts) is
    recorded in ``runs/<dag_id>/dag.json``.
    """
    root = Path(workspace_root).expanduser().resolve()
    dag_id = dag_id or f"{spec.name}-{spec_digest(spec)[:8]}"
    order = spec.topo_order()
    by_id = {node.id: node for node in spec.nodes}
    dag_dir = root / "runs" / dag_id
    dag_dir.mkdir(parents=True, exist_ok=True)
    sidecar = dag_dir / "dag.json"

    state: dict[str, Any] = {"dag_id": dag_id, "spec_digest": spec_digest(spec), "nodes": {}}
    if resume and sidecar.is_file():
        try:
            loaded = json.loads(sidecar.read_text(encoding="utf-8"))
            if loaded.get("spec_digest") == state["spec_digest"]:
                state = loaded
        except (ValueError, OSError):
            pass

    id_sets: dict[str, set[str]] = {}
    for node_id, saved in state.get("nodes", {}).items():
        if isinstance(saved, dict) and isinstance(saved.get("ids"), list):
            id_sets[node_id] = set(saved["ids"])

    for node_id in order:
        node = by_id[node_id]
        saved = state["nodes"].get(node_id) if isinstance(state.get("nodes"), dict) else None
        if isinstance(node, RunNode):
            run_id = f"{dag_id}-{node_id}"
            if resume and isinstance(saved, dict) and saved.get("run_id") == run_id and _run_complete(store, run_id):
                saved["cached"] = True
                continue
            if node.task_from:
                saved_cal = state["nodes"].get(node.task_from) if isinstance(state.get("nodes"), dict) else None
                revision = saved_cal.get("revision") if isinstance(saved_cal, dict) else None
                if not revision:
                    raise DagError(
                        f"run node '{node.id}' task_from '{node.task_from}' has no registered "
                        "revision (calibrate node did not apply)"
                    )
                task = store.get_task_revision(revision)
            else:
                assert node.task is not None
                task = _resolve_dag_task(node.task, store, root)
            upstream = [id_sets[ref] for ref in node.ids_from if ref in id_sets]
            only_ids = _combine_ids(upstream, node.ids_mode)
            input_path = (root / node.input).expanduser()
            items = load_input_items(
                input_path,
                id_column=node.id_column,
                text_column=node.text_column,
                title_column=node.title_column,
                uri_column=node.uri_column,
                only_ids=only_ids,
            )
            policy = node.policy
            packet = Engine(task=task, store=store, policy=policy).run_campaign(
                raw_items=items,
                run_id=run_id,
                input_path=str(input_path),
                concurrency=node.sessions,
                max_attempts=node.max_attempts,
                output_packet_path=dag_dir / node_id / "clean_packet.json",
            )
            state["nodes"][node_id] = {
                "kind": "run",
                "run_id": run_id,
                "verified": packet.get("total_verified_records", 0),
                "input_digest": packet.get("input_digest"),
            }
        elif isinstance(node, RescoreNode):
            run_id = node.run_id or f"{dag_id}-{node_id}"
            if resume and isinstance(saved, dict) and saved.get("run_id") == run_id and _run_complete(store, run_id):
                saved["cached"] = True
                continue
            _execute_rescore_node(node, run_id, state, store, root, dag_id)
        elif isinstance(node, CalibrateNode):
            if resume and isinstance(saved, dict) and saved.get("report"):
                saved["cached"] = True
                continue
            _execute_calibrate_node(node, state, store, root, dag_dir)
        elif isinstance(node, FilterNode):
            run_id = f"{dag_id}-{node.from_run}"
            snapshot = store.run_snapshot(run_id)
            records, _ = verified_records_from_snapshot(snapshot)
            selected = _filter_and_sort_records(
                records,
                sort=node.sort,
                top=node.top,
                claim_filter=node.filter,
            )
            ids = {r.item_id for r in selected}
            id_sets[node_id] = ids
            ids_path = dag_dir / node_id / "ids.json"
            ids_path.parent.mkdir(parents=True, exist_ok=True)
            ids_path.write_text(json.dumps(sorted(ids), indent=2), encoding="utf-8")
            state["nodes"][node_id] = {
                "kind": "filter",
                "from_run": run_id,
                "ids": sorted(ids),
                "count": len(ids),
            }
        elif isinstance(node, ExportNode):
            run_id = f"{dag_id}-{node.from_run}"
            snapshot = store.run_snapshot(run_id)
            dest = root / node.output if node.output else dag_dir / node_id / f"output.{node.format}"
            packet = export_clean_packet(
                snapshot,
                dest,
                export_format=node.format,
                sort=node.sort,
                top=node.top,
                rank=node.rank,
                claim_filter=node.filter,
            )
            state["nodes"][node_id] = {
                "kind": "export",
                "from_run": run_id,
                "output": str(dest.resolve()) if dest.is_absolute() else str((root / dest).resolve()),
                "records": packet.get("total_verified_records", 0),
            }
        else:  # Unreachable: DagSpec parsing admits only the five node kinds.
            raise DagError(f"unknown node kind for '{node_id}'")
        sidecar.write_text(json.dumps(state, indent=2), encoding="utf-8")

    sidecar.write_text(json.dumps(state, indent=2), encoding="utf-8")
    return state
