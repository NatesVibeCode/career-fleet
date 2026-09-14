import csv

import pytest

from harness_fleet.export import export_clean_packet
from harness_fleet.input_data import load_input_items
from harness_fleet.models import ClaimFilter, FilterClause, FilterOp, SortSpec, TaskSpec


def test_csv_import_with_custom_columns(tmp_path):
    csv_file = tmp_path / "companies.csv"
    csv_file.write_text(
        "domain,research,employee_count,industry\n"
        "google.com,Search engine and cloud provider,180000,Technology\n"
        "stripe.com,Online payment infrastructure platform,8000,Financial Services\n"
    )

    items = load_input_items(
        csv_file,
        id_column="domain",
        text_column="research",
    )

    assert len(items) == 2
    assert items[0].item_id == "google.com"
    assert items[0].text == "Search engine and cloud provider"
    assert items[0].metadata == {"employee_count": "180000", "industry": "Technology"}

    assert items[1].item_id == "stripe.com"
    assert items[1].text == "Online payment infrastructure platform"
    assert items[1].metadata == {"employee_count": "8000", "industry": "Financial Services"}


def test_csv_export_projection(tmp_path):
    task = TaskSpec(
        name="classify",
        claims_schema={
            "type": "object",
            "properties": {
                "category": {"type": "string"},
                "sentiment": {"type": "string"},
            },
            "required": ["category", "sentiment"],
            "additionalProperties": False,
        },
    )

    import hashlib
    import json

    task_payload = task.model_dump(mode="json", by_alias=True)
    task_revision = hashlib.sha256(
        json.dumps(task.revision_payload(), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    ).hexdigest()

    run_data = {
        "run_id": "test-run",
        "task": task_payload,
        "task_revision": task_revision,
        "input_digest": "1" * 64,
        "batches": {
            "b1": {
                "status": "verified",
                "result": [{
                    "item_id": "item-1",
                    "source_uri": "https://example.com/1",
                    "source_digest": "a" * 64,
                    "content_type": "text/plain",
                    "claims": {"category": "cloud", "sentiment": "positive"},
                    "quotes": [{
                        "slice_id": "full",
                        "start": 0,
                        "end": 20,
                        "text": "great cloud provider",
                    }],
                }],
            }
        },
        "model_runs": [{
            "id": "rec-1",
            "provider": "openrouter",
            "requested_route": "openrouter/free",
            "status": "complete",
            "cost": 0.0,
            "cost_status": "reported_zero",
            "usage": {"total_tokens": 15},
            "duration_seconds": 0.5,
        }],
    }

    csv_output = tmp_path / "results.csv"
    export_clean_packet(run_data, csv_output, export_format="csv")

    assert csv_output.is_file()
    with open(csv_output, encoding="utf-8") as f:
        reader = list(csv.DictReader(f))
        assert len(reader) == 1
        row = reader[0]
        assert row["item_id"] == "item-1"
        assert row["source_uri"] == "https://example.com/1"
        assert row["category"] == "cloud"
        assert row["sentiment"] == "positive"
        assert row["primary_quote_text"] == "great cloud provider"
        assert row["quote_count"] == "1"


def test_csv_export_sorting_ranking_and_filtering(tmp_path):
    import hashlib
    import json

    task = TaskSpec(
        name="score-task",
        claims_schema={
            "type": "object",
            "properties": {
                "score": {"type": "integer"},
                "passed": {"type": "boolean"},
                "tier": {"type": "string"},
            },
            "required": ["score", "passed", "tier"],
            "additionalProperties": False,
        },
    )
    task_payload = task.model_dump(mode="json", by_alias=True)
    task_revision = hashlib.sha256(
        json.dumps(task.revision_payload(), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    ).hexdigest()

    def make_item(item_id, score, passed, tier, quote):
        return {
            "item_id": item_id,
            "source_uri": f"https://example.com/{item_id}",
            "source_digest": "a" * 64,
            "content_type": "text/plain",
            "claims": {"score": score, "passed": passed, "tier": tier},
            "quotes": [{
                "slice_id": "full",
                "start": 0,
                "end": len(quote),
                "text": quote,
            }],
        }

    run_data = {
        "run_id": "scoring-run",
        "task": task_payload,
        "task_revision": task_revision,
        "input_digest": "1" * 64,
        "batches": {
            "b1": {
                "status": "verified",
                "result": [
                    make_item("stripe.com", 98, True, "tier_1", "migration off legacy v1 billing pipeline"),
                    make_item("unfit.co", 30, False, "unfit", "simple WordPress site with no infra"),
                    make_item("hyper_ai", 94, True, "tier_1", "hitting latency limits at 50k QPS"),
                    make_item("pinecone.io", 91, True, "tier_1", "scaling vector search across multi-tenant"),
                    make_item("small_app", 65, True, "tier_2", "using sqlite on single VPS"),
                ],
            }
        },
        "model_runs": [],
    }

    # Test 1: Sort by score descending, top 3, with rank column
    ranked_csv = tmp_path / "ranked.csv"
    export_clean_packet(
        run_data,
        ranked_csv,
        export_format="csv",
        sort=SortSpec(field="score"),
        top=3,
        rank=True,
    )
    assert ranked_csv.is_file()
    with open(ranked_csv, encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 3
    assert [r["rank"] for r in rows] == ["1", "2", "3"]
    assert [r["item_id"] for r in rows] == ["stripe.com", "hyper_ai", "pinecone.io"]
    assert [r["score"] for r in rows] == ["98", "94", "91"]

    # Test 2: Filter by passed=true
    survivors_csv = tmp_path / "survivors.csv"
    export_clean_packet(
        run_data,
        survivors_csv,
        export_format="csv",
        claim_filter=ClaimFilter(all=[FilterClause(field="passed", value=True)]),
    )
    with open(survivors_csv, encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 4
    assert "unfit.co" not in [r["item_id"] for r in rows]

    # Test 3: Filter by score>=90
    high_score_csv = tmp_path / "high_score.csv"
    export_clean_packet(
        run_data,
        high_score_csv,
        export_format="csv",
        claim_filter=ClaimFilter(all=[FilterClause(field="score", op=FilterOp.GTE, value=90)]),
        sort=SortSpec(field="score"),
        rank=True,
    )
    with open(high_score_csv, encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 3
    assert [r["item_id"] for r in rows] == ["stripe.com", "hyper_ai", "pinecone.io"]


def test_blank_text_row_fails_closed(tmp_path):
    from harness_fleet.input_data import InputDataError, load_input_items

    bad = tmp_path / "bad.csv"
    bad.write_text("item_id,text\ngood,real evidence here\nempty,\n")
    with pytest.raises(InputDataError, match="empty text column"):
        load_input_items(bad)


def test_only_ids_are_exact_by_default_with_fuzzy_opt_out(tmp_path):
    from harness_fleet.input_data import load_input_items

    # JSONL has no ID cleaning, so near-misses exercise matching directly.
    data = tmp_path / "data.jsonl"
    data.write_text('{"item_id": "hyper_ai", "text": "fast GPU infrastructure"}\n')
    assert load_input_items(data, only_ids={"hyper ai"}) == []
    assert len(load_input_items(data, only_ids={"hyper ai"}, fuzzy_ids=True)) == 1
    assert len(load_input_items(data, only_ids={"hyper_ai"})) == 1


def test_load_input_items_with_only_ids(tmp_path):
    all_accounts = tmp_path / "all_accounts.csv"
    all_accounts.write_text(
        "domain,research\n"
        "stripe.com,leading payment infrastructure\n"
        "hyper_ai,fast GPU infrastructure\n"
        "pinecone.io,vector search engine\n"
        "unfit.co,local bakery blog\n"
    )

    # 1. only_ids from CSV file (e.g. survivors from previous layer)
    survivors_file = tmp_path / "survivors.csv"
    survivors_file.write_text("item_id,passed\nstripe.com,true\nhyper_ai,true\n")

    items_from_file = load_input_items(all_accounts, only_ids=survivors_file)
    assert len(items_from_file) == 2
    assert {i.item_id for i in items_from_file} == {"stripe.com", "hyper_ai"}

    # 2. only_ids as comma-separated string
    items_from_str = load_input_items(all_accounts, only_ids="stripe.com, pinecone.io")
    assert len(items_from_str) == 2
    assert {i.item_id for i in items_from_str} == {"stripe.com", "pinecone.io"}

    # 3. only_ids as set
    items_from_set = load_input_items(all_accounts, only_ids={"hyper_ai"})
    assert len(items_from_set) == 1
    assert items_from_set[0].item_id == "hyper_ai"

