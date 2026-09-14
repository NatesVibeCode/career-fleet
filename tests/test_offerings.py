"""Unit and integration tests for offerings 1-11 enhancements.

Covers:
- Quickstart --demo offline execution
- Init --from-example schema inference from CSV
- Quote grounding repair: unicode NFKC, whitespace, curly quotes, fuzzy fallback
- Export format jsonl
- SQLite online db backup
- Input loaders: txt, md, html
- High-level Python SDK: harness_fleet.process
- Explicit --free-only policy flag
- Deprecation notice for harness-fleet invocation
"""
from __future__ import annotations

import argparse
import csv
import json
from pathlib import Path

import harness_fleet
from harness_fleet.catalog import PriceState, RouteCatalog
from harness_fleet.cli import (
    _extract_policy,
    cmd_db_backup,
    cmd_init,
    cmd_quickstart,
)
from harness_fleet.engine import Engine
from harness_fleet.export import export_clean_packet
from harness_fleet.grounding import normalize_grounding, verify_grounding
from harness_fleet.input_data import load_input_items
from harness_fleet.models import (
    CandidateExtractedItem,
    QuoteCandidate,
    RoutePolicy,
    SortSpec,
    TaskSpec,
)
from harness_fleet.packer import pack_items
from harness_fleet.store import HarnessStore
from harness_fleet.task import create_task_from_preset


def test_quickstart_demo(tmp_path: Path):
    run_id = "test-quickstart-demo"
    out_packet = tmp_path / "packet.json"
    db_path = tmp_path / "test.db"

    args = argparse.Namespace(
        demo=True,
        run_id=run_id,
        output=str(out_packet),
        db=str(db_path),
        json=True,
        global_json=False,
    )
    cmd_quickstart(args)

    assert out_packet.is_file()
    csv_path = out_packet.with_suffix(".csv")
    assert csv_path.is_file()

    data = json.loads(out_packet.read_text())
    assert data["format_version"] == "harness_fleet_v2"
    assert data["run_id"] == run_id
    assert data["total_verified_records"] > 0
    assert len(data["records"]) == data["total_verified_records"]


def test_init_from_example_csv(tmp_path: Path):
    csv_file = tmp_path / "training.csv"
    csv_file.write_text(
        "id,feedback,category\n"
        "1,Checkout button gave a 500 error,bug\n"
        "2,Shipping was fast and nicely packed,delivery\n"
        "3,Can you add dark mode?,feature_request\n"
        "4,Another bug in login page,bug\n"
    )
    db_path = tmp_path / "init_test.db"
    task_name = "customer_feedback"

    args = argparse.Namespace(
        name=task_name,
        preset="classify",
        from_example=str(csv_file),
        label_column="category",
        batch_size=4,
        sample=str(tmp_path / "sample.jsonl"),
        db=str(db_path),
        json=True,
        global_json=False,
    )
    cmd_init(args)

    store = HarnessStore(db_path)
    spec = store.get_task(task_name)
    assert spec.name == task_name
    schema = spec.claims_schema
    assert schema["type"] == "object"
    assert "label" in schema["properties"]
    assert "enum" in schema["properties"]["label"]
    assert set(schema["properties"]["label"]["enum"]) == {"bug", "delivery", "feature_request"}


def test_quote_repair_unicode_and_whitespace():
    # Source contains smart quotes, non-breaking space, em-dash
    source = 'The company stated: “Revenue grew by 24%—a new record.” All customers celebrated.'
    cards = pack_items([{"item_id": "item_1", "text": source}], max_slice_chars=6000)
    raw_card = cards[0]["items"][0]

    # Model returned straight quotes, regular dash, and collapsed whitespace
    model_quote = '"Revenue grew by 24%--a new record."'
    extracted = [
        CandidateExtractedItem(
            item_id="item_1",
            claims={"summary": "Record revenue growth"},
            quotes=[QuoteCandidate(slice_id="full", text=model_quote)],
        )
    ]

    normalized, err = normalize_grounding(extracted, [raw_card], min_quote_chars=10)
    assert err is None
    assert normalized is not None
    assert len(normalized) == 1
    repaired_quote = normalized[0].quotes[0]
    # The repaired quote text must match the source verbatim
    assert repaired_quote.text == '“Revenue grew by 24%—a new record.”'
    # And verify_grounding must pass cleanly
    ok, verify_err = verify_grounding(normalized, [raw_card], min_quote_chars=10)
    assert ok is True
    assert verify_err is None


def test_quote_repair_fuzzy_fallback():
    # Source has exact text
    source = "Enterprise customer reported critical failure on payment gateway cluster."
    cards = pack_items([{"item_id": "item_1", "text": source}], max_slice_chars=6000)
    raw_card = cards[0]["items"][0]

    # Model had a minor typo: "paymnt gateway cluster" instead of "payment gateway cluster"
    model_quote = "critical failure on paymnt gateway cluster."
    extracted = [
        CandidateExtractedItem(
            item_id="item_1",
            claims={"summary": "Gateway failure"},
            quotes=[QuoteCandidate(slice_id="full", text=model_quote)],
        )
    ]

    normalized, err = normalize_grounding(extracted, [raw_card], min_quote_chars=10)
    assert err is None
    assert normalized is not None
    assert len(normalized) == 1
    repaired_quote = normalized[0].quotes[0]
    assert "critical failure on payment gateway cluster." in repaired_quote.text

    ok, verify_err = verify_grounding(normalized, [raw_card], min_quote_chars=10)
    assert ok is True
    assert verify_err is None


def test_export_jsonl(tmp_path: Path):
    db_path = tmp_path / "export_test.db"
    out_packet = tmp_path / "demo_packet.json"

    # Run quickstart to generate a populated run
    args = argparse.Namespace(
        demo=True,
        run_id="jsonl-run",
        output=str(out_packet),
        db=str(db_path),
        json=True,
        global_json=False,
    )
    cmd_quickstart(args)

    store = HarnessStore(db_path)
    snapshot = store.run_snapshot("jsonl-run")

    jsonl_output = tmp_path / "exported.jsonl"
    export_clean_packet(snapshot, jsonl_output, export_format="jsonl")

    assert jsonl_output.is_file()
    lines = [line.strip() for line in jsonl_output.read_text().splitlines() if line.strip()]
    assert len(lines) == snapshot["total_items"]
    first = json.loads(lines[0])
    assert "item_id" in first
    assert "primary_quote_text" in first
    assert "quotes" in first


def test_db_backup(tmp_path: Path):
    db_path = tmp_path / "original.db"
    store = HarnessStore(db_path)
    spec = TaskSpec(name="backup_test", instructions="test")
    store.register_task(spec)

    backup_path = tmp_path / "subfolder" / "backup.db"
    args = argparse.Namespace(
        db=str(db_path),
        destination=str(backup_path),
        json=True,
        global_json=False,
    )
    cmd_db_backup(args)

    assert backup_path.is_file()
    assert backup_path.stat().st_size > 0

    backup_store = HarnessStore(backup_path)
    retrieved = backup_store.get_task("backup_test")
    assert retrieved.name == "backup_test"


def test_input_txt_md_html(tmp_path: Path):
    txt_file = tmp_path / "doc.txt"
    txt_file.write_text("Plain text content for analysis.")
    items_txt = load_input_items(txt_file)
    assert len(items_txt) == 1
    assert items_txt[0].item_id == "doc"
    assert items_txt[0].text == "Plain text content for analysis."

    md_file = tmp_path / "readme.md"
    md_file.write_text("# Markdown Title\n\nSome body text.")
    items_md = load_input_items(md_file)
    assert len(items_md) == 1
    assert "Markdown Title" in items_md[0].text

    html_file = tmp_path / "page.html"
    html_file.write_text(
        "<html><head><script>alert('skip');</script><style>body {color: red;}</style></head>"
        "<body><h1>Page Title</h1><p>Main content paragraphs here.</p></body></html>"
    )
    items_html = load_input_items(html_file)
    assert len(items_html) == 1
    assert "alert" not in items_html[0].text
    assert "color: red" not in items_html[0].text
    assert "Page Title" in items_html[0].text
    assert "Main content paragraphs here" in items_html[0].text


def test_high_level_process_sdk(tmp_path: Path):
    db_path = tmp_path / "sdk.db"
    catalog = RouteCatalog(db_path=db_path)
    catalog.add_route(
        route_id="demo/fake",
        provider="demo",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state="price_observed_zero",
    )

    task = TaskSpec(
        name="sdk_task",
        instructions="Extract summary.",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )

    policy = RoutePolicy(allowed_routes=["demo/fake"], free_only=True)
    input_items = [
        {"item_id": "item_1", "text": "Stripe provides developer payment infrastructure."},
        {"item_id": "item_2", "text": "Resend provides developer email infrastructure."},
    ]

    out_packet = tmp_path / "sdk_out.json"
    result = harness_fleet.process(
        task=task,
        input=input_items,
        run_id="sdk-demo-run",
        policy=policy,
        db=db_path,
        output=out_packet,
    )

    assert result["total_verified_records"] == 2
    assert out_packet.is_file()


def test_free_only_flag_and_policy():
    parser = argparse.ArgumentParser()
    from harness_fleet.cli import _policy_options

    _policy_options(parser)
    args = parser.parse_args(["--free-only"])
    policy = _extract_policy(args)

    assert policy is not None
    assert policy.free_only is True


def test_no_deprecation_shim():
    import harness_fleet.cli as cli_module

    assert not hasattr(cli_module, "_maybe_emit_deprecation_notice")


def test_account_research_pipeline(tmp_path: Path):
    db_path = tmp_path / "research_test.db"
    store = HarnessStore(db_path)
    catalog = RouteCatalog(db_path=store.path)
    catalog.add_route(
        route_id="demo/fake",
        provider="demo",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state=PriceState.PRICE_OBSERVED_ZERO.value,
        verification_source="test",
    )

    spec = create_task_from_preset("research-score-test", preset_name="score")
    store.register_task(spec)

    sample_csv = tmp_path / "samples.csv"
    with open(sample_csv, "w", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(["item_id", "text"])
        for i in range(10):
            writer.writerow([f"item_{i}", f"This is document number {i} providing sufficient text to extract verbatim proof from the source."])

    items = load_input_items(sample_csv)
    assert len(items) == 10

    engine = Engine(task=spec, store=store, policy=RoutePolicy(allowed_routes=["demo/fake"], free_only=True))
    packet_path = tmp_path / "accounts_packet.json"
    run_id = "test-accounts-pipeline"
    packet = engine.run_campaign(
        raw_items=items,
        run_id=run_id,
        input_path=str(sample_csv),
        concurrency=2,
        max_attempts=20,
        output_packet_path=packet_path,
        policy=RoutePolicy(allowed_routes=["demo/fake"], free_only=True),
    )
    assert packet["total_verified_records"] == 10

    # Export ranked target accounts deliverable
    ranked_csv = tmp_path / "ranked_target_accounts.csv"
    snapshot = store.run_snapshot(run_id)
    export_clean_packet(
        snapshot,
        ranked_csv,
        export_format="csv",
        sort=SortSpec(field="score"),
        top=5,
        rank=True,
    )

    assert ranked_csv.is_file()
    with open(ranked_csv, encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 5
    assert [r["rank"] for r in rows] == ["1", "2", "3", "4", "5"]
    for row in rows:
        assert row["item_id"]
        assert "score" in row
        assert "primary_quote_text" in row
        assert len(row["primary_quote_text"]) >= 15

