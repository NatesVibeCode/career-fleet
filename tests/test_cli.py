import json
from argparse import Namespace

from free_fleet import cli
from free_fleet.models import TaskSpec
from free_fleet.profile import IdealCompanyProfile
from free_fleet.store import BulkLanesStore


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

    task = BulkLanesStore(db).get_task("demo")
    assert set(task.claims_schema["properties"]) == {"priority", "reason"}
    assert (tmp_path / "demo.sample.jsonl").is_file()


def test_profile_command_persists_typed_ideal_company_profile(tmp_path, capsys):
    profile_path = tmp_path / "ideal_company_profile.json"
    db_path = tmp_path / "state.db"
    profile = IdealCompanyProfile(
        profile_name="Database Buyers",
        product_category="Database performance",
        required_stack=["PostgreSQL"],
    )
    profile.save(profile_path)

    cli.cmd_profile(cli.argparse.Namespace(
        path=str(profile_path), init=False, force=False, db=str(db_path), json=True,
    ))

    payload = json.loads(capsys.readouterr().out)
    assert payload["profile_kind"] == "ideal_company"
    stored = BulkLanesStore(db_path)
    assert stored.load_profile().model_dump() == profile.model_dump()
    assert stored.active_profile_revision_id() == payload["revision"]


def test_ideal_company_profile_accepts_interview_document_shape():
    profile = IdealCompanyProfile.model_validate({
        "icp_profile": {
            "product_category": "Database performance",
            "architectural_layer": "Postgres proxy",
            "required_stack": ["PostgreSQL"],
        },
        "calibrated_scoring_rubric": {"tier_1": "explicit pain"},
    })
    assert profile.product_category == "Database performance"
    assert profile.calibrated_scoring_rubric == {"tier_1": "explicit pain"}


def test_profile_path_is_attached_to_run_snapshot(tmp_path):
    profile_path = tmp_path / "ideal_company_profile.json"
    db_path = tmp_path / "state.db"
    profile = IdealCompanyProfile(profile_name="Run ICP")
    profile.save(profile_path)
    store = BulkLanesStore(db_path)
    revision = store.save_profile(IdealCompanyProfile.load(profile_path))
    task_revision = store.register_task(TaskSpec(name="run-profile-task"))
    store.create_run(
        "run-with-profile", task_revision, "input.jsonl", "a" * 64, 0, 1, 1, "out.json",
        profile_revision_id=revision,
    )

    assert store.run_snapshot("run-with-profile")["profile_revision_id"] == revision


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


def test_json_flag_works_before_command():
    args = cli.build_parser().parse_args(["--json", "tasks"])
    if args.global_json:
        args.json = True
    assert args.json is True


def test_presets_cover_each_named_bulk_job():
    assert set(cli.PRESETS) == {
        "account-research",
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
    from free_fleet.models import ExtractedItem, PackedBatch, SourceSlice
    from free_fleet.store import BulkLanesStore

    db = tmp_path / "run_test.db"
    store = BulkLanesStore(db)

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
    from free_fleet.packer import pack_items
    from free_fleet.models import ProviderReceipt
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
        filter_expr=None,
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
