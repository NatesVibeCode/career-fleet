"""DAG workflows: validation, ordering, lossless multi-stage funnels, resume."""
import csv
import json

import pytest
from pydantic import ValidationError

from harness_fleet.dag import DagError, DagSpec, run_dag
from harness_fleet.store import HarnessStore


def _spec(**kwargs):
    base = {"name": "t", "nodes": []}
    base.update(kwargs)
    return DagSpec.model_validate(base)


def test_duplicate_ids_rejected():
    with pytest.raises(ValidationError, match="duplicate"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv"},
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv"},
        ])


def test_unknown_reference_rejected():
    with pytest.raises(ValidationError, match="unknown"):
        _spec(nodes=[
            {"kind": "filter", "id": "f", "from_run": "ghost"},
        ])


def test_wrong_kind_reference_rejected():
    with pytest.raises(ValidationError, match="not a run or rescore node"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv"},
            {"kind": "export", "id": "e", "from_run": "e"},
        ])
    with pytest.raises(ValidationError, match="not a filter node"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv"},
            {"kind": "run", "id": "b", "task": "t", "input": "i.csv", "ids_from": ["a"]},
        ])


def test_cycle_rejected():
    with pytest.raises(ValidationError, match="cycle"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv",
             "ids_from": ["fa"]},
            {"kind": "filter", "id": "fa", "from_run": "a"},
            {"kind": "run", "id": "b", "task": "t", "input": "i.csv",
             "ids_from": ["fb"]},
            {"kind": "filter", "id": "fb", "from_run": "b"},
        ])
    # Self-edge via filter is a cycle too (filter -> run -> filter)
    with pytest.raises(ValidationError, match="cycle|itself"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.csv", "ids_from": ["f"]},
            {"kind": "filter", "id": "f", "from_run": "a"},
            {"kind": "run", "id": "b", "task": "t", "input": "i.csv", "ids_from": ["f"]},
        ])


def test_topo_order_respects_edges():
    spec = _spec(nodes=[
        {"kind": "export", "id": "out", "from_run": "l2"},
        {"kind": "run", "id": "l2", "task": "t", "input": "i.csv", "ids_from": ["l1f"]},
        {"kind": "filter", "id": "l1f", "from_run": "l1"},
        {"kind": "run", "id": "l1", "task": "t", "input": "i.csv"},
    ])
    assert spec.topo_order() == ["l1", "l1f", "l2", "out"]


def _write_items(path, n):
    with open(path, "w", encoding="utf-8", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["item_id", "text"])
        for i in range(n):
            writer.writerow([f"item_{i}", f"Document {i} provides sufficient source text for verbatim grounding checks."])


def _funnel_spec():
    return {
        "name": "funnel",
        "nodes": [
            {"kind": "run", "id": "l1", "task": "filter-demo", "input": "candidates.csv",
             "sessions": 2, "policy": {"allowed_routes": ["demo/fake"], "free_only": True}},
            {"kind": "filter", "id": "l1f", "from_run": "l1",
             "filter": {"all": [{"field": "passed", "value": True}]},
             "sort": {"field": "passed"}, "top": 3},
            {"kind": "run", "id": "l2", "task": "score-demo", "input": "candidates.csv",
             "ids_from": ["l1f"], "sessions": 2,
             "policy": {"allowed_routes": ["demo/fake"], "free_only": True}},
            {"kind": "export", "id": "out", "from_run": "l2", "format": "csv",
             "output": "ranked.csv", "sort": {"field": "score"}, "rank": True},
        ],
    }


def _setup_workspace(tmp_path):
    from harness_fleet.catalog import PriceState, RouteCatalog
    from harness_fleet.task import create_task_from_preset

    db = tmp_path / "t.db"
    store = HarnessStore(db)
    RouteCatalog(db_path=store.path).add_route(
        route_id="demo/fake", provider="demo",
        cost_per_1k_input=0.0, cost_per_1k_output=0.0, enabled=True,
        price_state=PriceState.PRICE_OBSERVED_ZERO.value,
        verification_source="test",
    )
    store.register_task(create_task_from_preset("filter-demo", preset_name="filter"))
    store.register_task(create_task_from_preset("score-demo", preset_name="score"))
    _write_items(tmp_path / "candidates.csv", 6)
    return store


def test_end_to_end_funnel_is_lossless(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    store = _setup_workspace(tmp_path)
    spec = DagSpec.model_validate(_funnel_spec())

    state = run_dag(spec, store, workspace_root=tmp_path, dag_id="f1")

    assert state["nodes"]["l1"]["verified"] == 6
    assert state["nodes"]["l1f"]["count"] == 3
    assert state["nodes"]["l2"]["verified"] == 3
    assert state["nodes"]["out"]["records"] == 3

    # Lineage sidecar pins spec digest and per-node runs
    sidecar = json.loads((tmp_path / "runs" / "f1" / "dag.json").read_text())
    assert sidecar["spec_digest"]
    assert sidecar["nodes"]["l2"]["run_id"] == "f1-l2"

    # Export carries full drill-through (offsets survive every hop)
    rows = list(csv.DictReader(open(tmp_path / "ranked.csv", encoding="utf-8")))
    assert len(rows) == 3
    assert rows[0]["rank"] == "1"


def test_resume_reuses_completed_run_nodes(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    store = _setup_workspace(tmp_path)
    spec = DagSpec.model_validate(_funnel_spec())

    first = run_dag(spec, store, workspace_root=tmp_path, dag_id="f1")
    second = run_dag(spec, store, workspace_root=tmp_path, dag_id="f1")

    assert first["nodes"]["l1"]["run_id"] == second["nodes"]["l1"]["run_id"] == "f1-l1"
    assert second["nodes"]["l2"]["verified"] == 3


def test_cli_dry_run_validates(tmp_path, capsys):
    from argparse import Namespace

    from harness_fleet import cli

    spec_path = tmp_path / "f.json"
    spec_path.write_text(json.dumps(_funnel_spec()))
    cli.cmd_dag(Namespace(spec=str(spec_path), dag_id=None, dry_run=True,
                          no_resume=False, workspace_root=str(tmp_path),
                          db=str(tmp_path / "t.db"), json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["order"] == ["l1", "l1f", "l2", "out"]


def test_rescore_node_needs_exactly_one_parent():
    with pytest.raises(ValidationError, match="exactly one of 'from_run' or 'parent_run'"):
        _spec(nodes=[
            {"kind": "rescore", "id": "r", "input": "i.jsonl"},
        ])
    with pytest.raises(ValidationError, match="exactly one of 'from_run' or 'parent_run'"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.jsonl"},
            {"kind": "rescore", "id": "r", "from_run": "a", "parent_run": "other", "input": "i.jsonl"},
        ])
    with pytest.raises(ValidationError, match="not a run or rescore node"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.jsonl"},
            {"kind": "filter", "id": "f", "from_run": "a"},
            {"kind": "rescore", "id": "r", "from_run": "f", "input": "i.jsonl"},
        ])


def test_run_task_from_validation():
    with pytest.raises(ValidationError, match="exactly one of 'task' or 'task_from'"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "task_from": "c", "input": "i.jsonl"},
        ])
    with pytest.raises(ValidationError, match="exactly one of 'task' or 'task_from'"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "input": "i.jsonl"},
        ])
    with pytest.raises(ValidationError, match="not a calibrate node"):
        _spec(nodes=[
            {"kind": "run", "id": "a", "task": "t", "input": "i.jsonl"},
            {"kind": "run", "id": "b", "task_from": "a", "input": "i.jsonl"},
        ])


def test_calibrate_node_params_validation():
    with pytest.raises(ValidationError, match="not in"):
        _spec(nodes=[
            {"kind": "calibrate", "id": "c", "task": "t", "input": "i.jsonl",
             "route": "r/x", "params": ["nope"]},
        ])
    with pytest.raises(ValidationError, match="at least one param"):
        _spec(nodes=[
            {"kind": "calibrate", "id": "c", "task": "t", "input": "i.jsonl",
             "route": "r/x", "params": []},
        ])


def test_filter_accepts_rescore_upstream():
    spec = _spec(nodes=[
        {"kind": "run", "id": "a", "task": "t", "input": "i.jsonl"},
        {"kind": "rescore", "id": "r", "from_run": "a", "input": "i.jsonl"},
        {"kind": "filter", "id": "f", "from_run": "r"},
    ])
    assert spec.topo_order() == ["a", "r", "f"]


def _write_jsonl(path, rows):
    with open(path, "w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")


def _score_workspace(tmp_path):
    from harness_fleet.catalog import PriceState, RouteCatalog
    from harness_fleet.task import create_task_from_preset

    db = tmp_path / "t.db"
    store = HarnessStore(db)
    RouteCatalog(db_path=store.path).add_route(
        route_id="demo/fake", provider="demo",
        cost_per_1k_input=0.0, cost_per_1k_output=0.0, enabled=True,
        price_state=PriceState.PRICE_OBSERVED_ZERO.value,
        verification_source="test",
    )
    store.register_task(create_task_from_preset("score-demo", preset_name="score"))
    return store


def test_rescore_lineage_end_to_end(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    store = _score_workspace(tmp_path)
    _write_jsonl(tmp_path / "round1.jsonl", [{
        "item_id": "acme",
        "text": "Acme is migrating its platform to Kubernetes this quarter with senior hiring underway.",
        "source_uri": "https://boards.greenhouse.io/acme/1",
        "metadata": {"entity": "acme", "captured_at": "2026-08-01T00:00:00+00:00"},
    }])
    _write_jsonl(tmp_path / "round2.jsonl", [{
        "item_id": "acme",
        "text": "Acme was acquired this month; the combined group doubles platform investment this year.",
        "source_uri": "https://example.com/press/acme-acquired",
        "metadata": {"entity": "acme", "captured_at": "2026-09-01T00:00:00+00:00"},
    }])
    policy = {"allowed_routes": ["demo/fake"], "free_only": True}
    spec = DagSpec.model_validate({"name": "lineage", "nodes": [
        {"kind": "run", "id": "l1", "task": "score-demo", "input": "round1.jsonl", "policy": policy},
        {"kind": "rescore", "id": "r2", "from_run": "l1", "input": "round2.jsonl", "policy": policy},
        {"kind": "filter", "id": "f", "from_run": "r2"},
    ]})
    assert spec.topo_order() == ["l1", "r2", "f"]
    state = run_dag(spec, store, workspace_root=tmp_path, dag_id="g1")

    assert state["nodes"]["l1"]["run_id"] == "g1-l1"
    assert state["nodes"]["r2"]["parent_run"] == "g1-l1"
    child = store.run_snapshot("g1-r2")
    assert child["parent_run_id"] == "g1-l1"
    assert state["nodes"]["r2"]["verified"] == 1
    assert state["nodes"]["f"]["count"] == 1
    rows = store.get_entity_history("acme")
    assert [row["run_id"] for row in rows] == ["g1-l1", "g1-r2"]

    rerun = run_dag(spec, store, workspace_root=tmp_path, dag_id="g1")
    assert rerun["nodes"]["r2"].get("cached") is True


def test_calibrate_apply_threads_revision_into_run(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    store = _score_workspace(tmp_path)
    _write_jsonl(tmp_path / "labeled.jsonl", [{
        "item_id": f"s{i}",
        "text": f"Acme initiative {i} migrates billing to Kafka with verified stack evidence present.",
        "metadata": {"score": 100},
    } for i in range(6)])
    policy = {"allowed_routes": ["demo/fake"], "free_only": True}
    spec = DagSpec.model_validate({"name": "fit", "nodes": [
        {"kind": "calibrate", "id": "cal", "task": "score-demo", "input": "labeled.jsonl",
         "route": "demo/fake", "params": ["points"], "apply": True},
        {"kind": "run", "id": "scored", "task_from": "cal", "input": "labeled.jsonl", "policy": policy},
    ]})
    state = run_dag(spec, store, workspace_root=tmp_path, dag_id="g2")

    report = state["nodes"]["cal"]["report"]
    assert report["fitted_train"]["mae"] == 0.0
    revision = state["nodes"]["cal"]["revision"]
    assert revision and report["new_revision"] == revision
    assert state["nodes"]["scored"]["verified"] == 6
    from harness_fleet.store import digest_json

    scored_task = store.get_run_task("g2-scored")
    assert digest_json(scored_task.revision_payload()) == revision
    assert scored_task.checklist == report["points"]
    assert json.loads((tmp_path / "runs" / "g2" / "cal" / "report.json").read_text())["task"] == "score-demo"


def test_task_from_without_apply_fails_closed(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    store = _score_workspace(tmp_path)
    _write_jsonl(tmp_path / "labeled.jsonl", [{
        "item_id": f"s{i}",
        "text": f"Acme initiative {i} migrates billing to Kafka with verified stack evidence present.",
        "metadata": {"score": 100},
    } for i in range(2)])
    spec = DagSpec.model_validate({"name": "fit", "nodes": [
        {"kind": "calibrate", "id": "cal", "task": "score-demo", "input": "labeled.jsonl",
         "route": "demo/fake", "params": ["points"], "apply": False},
        {"kind": "run", "id": "scored", "task_from": "cal", "input": "labeled.jsonl"},
    ]})
    with pytest.raises(DagError, match="did not apply"):
        run_dag(spec, store, workspace_root=tmp_path, dag_id="g3")
