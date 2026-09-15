import json
from argparse import Namespace

from harness_fleet import cli
from harness_fleet.profile import IdealCompanyProfile
from harness_fleet.store import HarnessStore


def test_init_registers_task_and_writes_typed_sample(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    db = tmp_path / "state.db"
    cli.cmd_init(Namespace(
        name="demo",
        preset="triage",
        batch_size=4,
        sample=None,
        db=str(db),
        json=True,
    ))

    task = HarnessStore(db).get_task("demo")
    assert set(task.claims_schema["properties"]) == {"priority", "reason"}
    assert (tmp_path / "demo.sample.jsonl").is_file()


def test_validate_is_offline_and_strict(tmp_path, capsys):
    db = tmp_path / "state.db"
    input_path = tmp_path / "input.jsonl"
    cli.cmd_init(Namespace(
        name="demo", preset="classify", batch_size=4,
        sample=str(input_path), db=str(db), json=True,
    ))
    capsys.readouterr()

    cli.cmd_validate(Namespace(task="demo", input=str(input_path), db=str(db), json=True))

    payload = json.loads(capsys.readouterr().out)
    assert payload["valid"] is True
    assert payload["input_items"] == 1


def test_validate_reports_slicing_caveats_in_the_typed_report(tmp_path, capsys):
    """A JSON consumer must see the partial-window warning, not just stderr."""
    from harness_fleet.models import TaskSpec

    db = tmp_path / "state.db"
    store = HarnessStore(db)
    store.register_task(TaskSpec(
        name="sliced",
        instructions="Classify each record.",
        batch_size=4,
        max_slice_chars=300,
        claims_schema={
            "type": "object",
            "properties": {"ok": {"type": "boolean"}},
            "additionalProperties": False,
        },
    ))

    input_path = tmp_path / "input.jsonl"
    input_path.write_text(
        json.dumps({"item_id": "long-1", "text": "Sentence about Kafka. " * 200}) + "\n",
        encoding="utf-8",
    )

    cli.cmd_validate(Namespace(task="sliced", input=str(input_path), db=str(db), json=True))

    payload = json.loads(capsys.readouterr().out)
    assert payload["valid"] is True
    assert payload["input_items"] == 1
    assert payload["errors"], "the slicing caveat must reach the typed report"
    assert "max_slice_chars=300" in payload["errors"][0]


def test_profile_command_persists_ideal_company_profile(tmp_path, capsys):
    profile_path = tmp_path / "ideal_company_profile.json"
    db_path = tmp_path / "state.db"
    profile = IdealCompanyProfile(profile_name="Database Buyers", required_stack=["PostgreSQL"])
    profile.save(profile_path)

    cli.cmd_profile(Namespace(path=str(profile_path), init=False, force=False, db=str(db_path), json=True))

    payload = json.loads(capsys.readouterr().out)
    stored = HarnessStore(db_path)
    assert payload["profile_kind"] == "ideal_company"
    assert stored.load_profile().model_dump() == profile.model_dump()
    assert stored.active_profile_revision_id() == payload["revision"]


def test_json_flag_works_before_command():
    args = cli.build_parser().parse_args(["--json", "tasks"])
    if args.global_json:
        args.json = True
    assert args.json is True


def test_presets_cover_each_named_bulk_job():
    assert set(cli.PRESETS) == {
        "career-screening",
        "classify",
        "extract",
        "filter",
        "score",
        "summarize",
        "triage",
    }


def test_routes_add_and_list_cli(tmp_path, capsys):
    db = tmp_path / "routes_test.db"
    cli.cmd_routes(Namespace(
        action="add",
        route_id="local/test-model",
        add=None,
        provider="openai_compatible",
        free=True,
        input_cost=0.0,
        output_cost=0.0,
        disable=False,
        all=False,
        refresh=False,
        db=str(db),
        json=True,
    ))
    payload = json.loads(capsys.readouterr().out)
    assert payload["status"] == "added"
    assert payload["route"]["id"] == "local/test-model"

    cli.cmd_routes(Namespace(
        action="list",
        route_id=None,
        add=None,
        provider=None,
        free=False,
        disable=False,
        all=False,
        refresh=False,
        db=str(db),
        json=True,
    ))
    list_payload = json.loads(capsys.readouterr().out)
    assert list_payload["count"] == 1
    assert list_payload["routes"][0]["id"] == "local/test-model"


def test_routes_add_cost_safety_defaults(tmp_path, capsys):
    db = tmp_path / "routes_safety.db"
    # Adding a route without --free or explicit costs must default to unknown price state and None costs
    cli.cmd_routes(Namespace(
        action="add",
        route_id="groq/llama-3.3-70b-versatile",
        add=None,
        provider="groq",
        free=False,
        input_cost=None,
        output_cost=None,
        disable=False,
        all=False,
        refresh=False,
        db=str(db),
        json=True,
    ))
    payload = json.loads(capsys.readouterr().out)
    assert payload["status"] == "added"
    assert payload["route"]["price_state"] == "unknown"
    assert payload["route"]["cost_per_1k_input"] is None
    assert payload["route"]["cost_per_1k_output"] is None

    # Adding a route with --free must register as price_observed_zero and 0.0
    cli.cmd_routes(Namespace(
        action="add",
        route_id="ollama/llama3.2:latest",
        add=None,
        provider="ollama",
        free=True,
        input_cost=None,
        output_cost=None,
        disable=False,
        all=False,
        refresh=False,
        db=str(db),
        json=True,
    ))
    payload = json.loads(capsys.readouterr().out)
    assert payload["status"] == "added"
    assert payload["route"]["price_state"] == "price_observed_zero"
    assert payload["route"]["cost_per_1k_input"] == 0.0
    assert payload["route"]["cost_per_1k_output"] == 0.0


def test_cli_policy_flag_parsing():
    parser = cli.build_parser()

    # Comma-separated list and order
    args = parser.parse_args([
        "run", "demo", "--input", "in.jsonl",
        "--openrouter-providers", "Anthropic,Together",
        "--openrouter-order", "latency",
        "--max-request-cost", "0.05",
    ])
    policy = cli._extract_policy(args)
    assert policy.openrouter_providers == ["Anthropic", "Together"]
    assert policy.openrouter_order == ["latency"]
    assert policy.max_request_cost == 0.05

    # Repeatable flags
    args2 = parser.parse_args([
        "run", "demo", "--input", "in.jsonl",
        "--openrouter-provider", "Together",
        "--openrouter-provider", "DeepInfra",
    ])
    policy2 = cli._extract_policy(args2)
    assert policy2.openrouter_providers == ["Together", "DeepInfra"]


def test_export_and_status_cli(tmp_path, capsys, monkeypatch):
    monkeypatch.chdir(tmp_path)
    import csv

    from harness_fleet.store import HarnessStore

    db = tmp_path / "run_test.db"
    store = HarnessStore(db)

    # Register task
    cli.cmd_init(Namespace(
        name="test-triage",
        preset="triage",
        batch_size=2,
        sample=None,
        db=str(db),
        json=True,
    ))
    capsys.readouterr()

    # Create run in store
    task = store.get_task("test-triage")
    rev_id = store.register_task(task)
    run_id = "test-run-1"
    store.create_run(
        run_id=run_id,
        task_revision_id=rev_id,
        input_path="input.csv",
        input_digest="d" * 64,
        total_items=1,
        max_attempts=10,
        batch_size=2,
        output_path="out.json",
    )
    from harness_fleet.models import ProviderReceipt
    from harness_fleet.packer import pack_items
    batch = pack_items([{"item_id": "item-1", "text": "broken button error"}], batch_size=2)[0]
    store.enqueue_batches(run_id, [batch], max_attempts_per_batch=5)
    lease = store.lease_batch(run_id, "worker-1")
    assert lease is not None
    receipt = ProviderReceipt(
        id="rec-1",
        provider="openai_compatible",
        requested_route="local/test-model",
        status="complete",
        cost=0.0,
        cost_status="reported_zero",
        usage={"total_tokens": 10},
        duration_seconds=0.2,
    )
    store.complete_batch(
        run_id=run_id,
        attempt_id=lease["attempt_id"],
        worker_id="worker-1",
        results=[{
            "item_id": "item-1",
            "source_digest": batch["items"][0]["source_digest"],
            "content_type": "text/plain",
            "claims": {"priority": "high", "reason": "broken button"},
            "quotes": [{"slice_id": "full", "start": 0, "end": 6, "text": "broken"}],
        }],
        receipt=receipt,
    )

    # Test status CLI
    cli.cmd_status(Namespace(run_id=run_id, watch=False, interval=1.0, db=str(db), json=True))
    status_out = json.loads(capsys.readouterr().out)
    assert status_out["run_id"] == run_id
    assert status_out["verified_items"] == 1

    # Test export CSV CLI
    csv_file = tmp_path / "exported.csv"
    cli.cmd_export(Namespace(run_id=run_id, format="csv", output=str(csv_file), db=str(db), json=True))
    export_out = json.loads(capsys.readouterr().out)
    assert export_out["format"] == "csv"
    assert csv_file.is_file()

    with open(csv_file, encoding="utf-8") as f:
        reader = list(csv.DictReader(f))
        assert len(reader) == 1
        assert reader[0]["item_id"] == "item-1"
        assert reader[0]["priority"] == "high"
        assert reader[0]["reason"] == "broken button"
        assert reader[0]["primary_quote_text"] == "broken"

    # Test export with sort and rank CLI
    ranked_csv = tmp_path / "ranked.csv"
    cli.cmd_export(Namespace(
        run_id=run_id,
        format="csv",
        output=str(ranked_csv),
        sort_by="priority",
        desc=True,
        top=1,
        rank=True,
        filter='{"all": [{"field": "priority", "value": "high"}]}',
        db=str(db),
        json=True,
    ))
    assert ranked_csv.is_file()
    with open(ranked_csv, encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 1
    assert rows[0]["rank"] == "1"
    assert rows[0]["item_id"] == "item-1"


def test_init_presets_score_and_account_research(tmp_path, capsys):
    db = tmp_path / "init_test.db"

    # Test score preset
    cli.cmd_init(Namespace(
        name="score-demo",
        preset="score",
        from_example=None,
        label_column=None,
        batch_size=5,
        sample=str(tmp_path / "score_sample.jsonl"),
        db=str(db),
        json=True,
    ))
    out = json.loads(capsys.readouterr().out)
    assert out["created"] is True
    assert out["task"] == "score-demo"
    assert "score" in out["claims_schema"]["properties"]
    assert "reason" in out["claims_schema"]["properties"]

    # Test filter preset
    cli.cmd_init(Namespace(
        name="filter-demo",
        preset="filter",
        from_example=None,
        label_column=None,
        batch_size=5,
        sample=str(tmp_path / "filter_sample.jsonl"),
        db=str(db),
        json=True,
    ))
    out = json.loads(capsys.readouterr().out)
    assert out["created"] is True
    assert "passed" in out["claims_schema"]["properties"]



def _write_jsonl(path, rows):
    with open(path, "w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")


def test_rescore_links_lineage_and_history(tmp_path, monkeypatch, capsys):
    """cmd_rescore scores fresh evidence under a new run and history shows both rounds."""
    from harness_fleet.catalog import RouteCatalog

    monkeypatch.chdir(tmp_path)
    db = tmp_path / "state.db"
    cli.cmd_init(Namespace(
        name="rescore-demo", preset="score", batch_size=4,
        source_weight=[], half_life=[],
        sample=str(tmp_path / "sample.jsonl"), db=str(db), json=True,
    ))
    capsys.readouterr()
    catalog = RouteCatalog(db_path=db)
    catalog.add_route(
        "demo/fake", provider="demo",
        cost_per_1k_input=0.0, cost_per_1k_output=0.0,
        enabled=True, price_state="price_observed_zero",
    )
    round1 = tmp_path / "round1.jsonl"
    _write_jsonl(round1, [{
        "item_id": "acme-r1",
        "text": "Acme is migrating its platform to Kubernetes this quarter with senior hiring underway.",
        "source_uri": "https://boards.greenhouse.io/acme/1",
        "metadata": {"entity": "acme", "captured_at": "2026-08-01T00:00:00+00:00"},
    }])
    run_ns = dict(
        task="rescore-demo", input=str(round1), route=["demo/fake"],
        id_column=None, text_column=None, title_column=None, uri_column=None,
        only_ids=None, only_ids_fuzzy=False, sessions=1, max_attempts=10,
        output=None, profile=None, use_active_profile=False,
        workspace_root=".", db=str(db), json=True,
    )
    cli.cmd_run(Namespace(run_id="round-1", **run_ns))
    capsys.readouterr()
    round2 = tmp_path / "round2.jsonl"
    _write_jsonl(round2, [{
        "item_id": "acme-r2",
        "text": "Acme was acquired; the combined group is doubling platform investment this year.",
        "source_uri": "https://example.com/press/acme-acquired",
        "metadata": {"entity": "acme", "captured_at": "2026-09-01T00:00:00+00:00"},
    }])
    cli.cmd_rescore(Namespace(
        parent_run="round-1", task=None, input=str(round2), route=["demo/fake"],
        id_column=None, text_column=None, title_column=None, uri_column=None,
        only_ids=None, only_ids_fuzzy=False, sessions=1, max_attempts=10,
        run_id="round-2", output=None, profile=None, use_active_profile=False,
        workspace_root=".", db=str(db), json=True,
    ))
    out = json.loads(capsys.readouterr().out)
    assert out["run_id"] == "round-2" and out["parent_run"] == "round-1"
    assert out["result"]["total_verified_records"] == 1

    store = HarnessStore(db)
    assert store.run_snapshot("round-2")["parent_run_id"] == "round-1"
    rows = store.get_entity_history("acme")
    assert [row["run_id"] for row in rows] == ["round-1", "round-2"]
    assert all(row["score"] == 100 for row in rows)

    cli.cmd_history(Namespace(entity="acme", db=str(db), json=True))
    history = json.loads(capsys.readouterr().out)
    assert history["entity"] == "acme" and len(history["rounds"]) == 2
