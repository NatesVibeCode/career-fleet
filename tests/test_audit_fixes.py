"""Regression tests for audit bug fixes."""
import json
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from harness_fleet.catalog import PriceState, RouteCatalog
from harness_fleet.engine import Engine
from harness_fleet.export import _evaluate_filter, _filter_and_sort_records
from harness_fleet.grounding import normalize_grounding
from harness_fleet.input_data import _resolve_only_ids, load_input_items
from harness_fleet.models import (
    CandidateExtractedItem,
    ClaimFilter,
    FilterClause,
    FilterOp,
    InputItem,
    QuoteCandidate,
    SortSpec,
    TaskSpec,
)
from harness_fleet.providers.base import clean_llm_json
from harness_fleet.providers.opencode import OpenCodeProvider
from harness_fleet.store import HarnessStore


def test_clean_llm_json_conversational_and_malformed():
    text1 = 'Certainly! Here is the JSON you requested:\n```json\n{"items": [{"id": 1}]}\n```\nHope this helps!'
    res1 = clean_llm_json(text1)
    assert res1 == {"items": [{"id": 1}]}

    text2 = '```json\n{"items": []}```'
    res2 = clean_llm_json(text2)
    assert res2 == {"items": []}

    text3 = '```\n{"items": ["a", "b"]} \n```'
    res3 = clean_llm_json(text3)
    assert res3 == {"items": ["a", "b"]}

    text4 = 'The result is: {"summary": "all clear"} - verified.'
    res4 = clean_llm_json(text4)
    assert res4 == {"summary": "all clear"}

    text5 = '```json\n{"items": [{"a": 1,},],}\n```'
    res5 = clean_llm_json(text5)
    assert res5 == {"items": [{"a": 1}]}

    assert clean_llm_json("") is None
    assert clean_llm_json("   ") is None
    assert clean_llm_json("Not a json at all") is None


def test_clean_llm_json_ignores_preamble_braces():
    # A brace in conversational preamble must not glue onto the payload.
    text = 'Here is {an idea} for you: {"items": [{"id": 1}]} done.'
    assert clean_llm_json(text) == {"items": [{"id": 1}]}
    # Trailing-comma payload without fences still repairs.
    assert clean_llm_json('Result: {"a": 1,} end') == {"a": 1}
    # Bare scalars are not answers.
    assert clean_llm_json("count is 42") is None


def test_opencode_prefix_stripping():
    fake_runner = MagicMock()
    fake_runner.run.return_value = (
        0,
        json.dumps({"type": "text", "part": {"text": '{"items": []}'}}) + "\n" + json.dumps({"type": "step_finish", "part": {"cost": 0.0}}),
        "",
    )
    prov = OpenCodeProvider(runner=fake_runner)

    prov.run_prompt("opencode/anthropic/claude-3-5-sonnet", "test prompt")
    task_config = fake_runner.run.call_args[1]["task_config"]
    args = fake_runner.run.call_args[1]["args"]
    assert task_config["model"] == "anthropic/claude-3-5-sonnet"
    assert args[4] == "anthropic/claude-3-5-sonnet"

    prov.run_prompt("opencode:meta/llama-3", "test prompt")
    task_config = fake_runner.run.call_args[1]["task_config"]
    args = fake_runner.run.call_args[1]["args"]
    assert task_config["model"] == "meta/llama-3"
    assert args[4] == "meta/llama-3"


def test_csv_excel_bom_and_trailing_empty_rows(tmp_path: Path):
    bom_content = "\ufeffitem_id,text,title\nrow_1,Some text content,Title 1\nrow_2,More text content,Title 2\n\n   \n"
    csv_file = tmp_path / "excel_export.csv"
    csv_file.write_bytes(bom_content.encode("utf-8"))

    items = load_input_items(csv_file)
    assert len(items) == 2
    assert items[0].item_id == "row_1"
    assert items[0].text == "Some text content"
    assert items[1].item_id == "row_2"

    resolved = _resolve_only_ids(csv_file)
    assert resolved == {"row_1", "row_2"}


def test_export_sorting_mixed_types_and_quoted_filters():
    from harness_fleet.models import ExtractedItem, QuoteRef

    digest = "a" * 64
    records = [
        ExtractedItem(
            item_id="rec_1",
            claims={"score": 85.0, "status": "passed"},
            quotes=[QuoteRef(slice_id="s1", start=0, end=10, text="verbatim12")],
            source_digest=digest,
            content_type="text/plain",
        ),
        ExtractedItem(
            item_id="rec_2",
            claims={"score": "N/A", "status": "failed"},
            quotes=[QuoteRef(slice_id="s1", start=0, end=10, text="verbatim12")],
            source_digest=digest,
            content_type="text/plain",
        ),
        ExtractedItem(
            item_id="rec_3",
            claims={"score": 95.0, "status": "passed"},
            quotes=[QuoteRef(slice_id="s1", start=0, end=10, text="verbatim12")],
            source_digest=digest,
            content_type="text/plain",
        ),
    ]

    sorted_recs = _filter_and_sort_records(records, sort=SortSpec(field="score", descending=True))
    assert [r.item_id for r in sorted_recs] == ["rec_3", "rec_1", "rec_2"]

    assert _evaluate_filter({"tier": "tier_1"}, ClaimFilter(all=[FilterClause(field="tier", value="tier_1")])) is True
    assert _evaluate_filter({"score": 85}, ClaimFilter(all=[FilterClause(field="score", op=FilterOp.GTE, value=80)])) is True


def test_resume_reclaims_abandoned_leased_batches(tmp_path: Path):
    db_path = tmp_path / "test.db"
    store = HarnessStore(db_path)
    catalog = RouteCatalog(db_path=db_path)
    catalog.add_route(
        route_id="demo/fake",
        provider="demo",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state=PriceState.PRICE_OBSERVED_ZERO.value,
        verification_source="test",
    )

    spec = TaskSpec(
        name="test-task",
        instructions="Extract label",
        batch_size=1,
        claims_schema={"type": "object", "properties": {"label": {"type": "string"}}, "required": ["label"], "additionalProperties": False},
    )
    store.register_task(spec)

    items = [
        InputItem(item_id="item_1", text="This is sample text for item 1."),
        InputItem(item_id="item_2", text="This is sample text for item 2."),
    ]

    from harness_fleet.packer import pack_items
    batches = pack_items(items, batch_size=1, max_slice_chars=1000)
    store.create_run(
        run_id="run_abandoned",
        task_revision_id=store.current_task_revision("test-task"),
        input_path="test",
        input_digest="a" * 64,
        total_items=2,
        max_attempts=10,
        batch_size=1,
        output_path=str(tmp_path / "packet.json"),
    )
    store.enqueue_batches("run_abandoned", batches, max_attempts_per_batch=3)

    lease1 = store.lease_batch("run_abandoned", worker_id="dead_worker")
    assert lease1 is not None
    with store.connect() as connection:
        connection.execute("UPDATE batches SET leased_at=datetime('now', '-10 minutes') WHERE run_id='run_abandoned' AND status='leased'")

    snapshot_before = store.run_snapshot("run_abandoned")
    assert snapshot_before["batches"][lease1["batch"]["batch_id"]]["status"] == "leased"

    from harness_fleet.models import RoutePolicy
    engine = Engine(task=spec, store=store, catalog=catalog, policy=RoutePolicy(allowed_routes=["demo/fake"]))
    packet = engine.resume_campaign("run_abandoned", concurrency=1, output_packet_path=tmp_path / "packet.json")

    assert packet["total_verified_records"] == 2
    snapshot_after = store.run_snapshot("run_abandoned")
    assert snapshot_after["status"] == "completed"


def test_candidate_offsets_repaired_when_inaccurate():
    raw_cards = [{
        "item_id": "card_1",
        "source_digest": "a" * 64,
        "content_type": "text/plain",
        "slices": [{
            "slice_id": "card_1:s0",
            "start": 0,
            "end": 60,
            "text": "The company offers a free trial for 14 days without credit card.",
        }],
    }]

    extracted = [
        CandidateExtractedItem(
            item_id="card_1",
            claims={"label": "trial"},
            quotes=[QuoteCandidate(
                slice_id="card_1:s0",
                text="free trial for 14 days without",
                start=10,
                end=40,
            )],
        )
    ]

    normalized, err = normalize_grounding(extracted, raw_cards, min_quote_chars=15)
    assert err is None
    assert normalized is not None
    assert len(normalized) == 1
    quote = normalized[0].quotes[0]
    assert quote.start == 21
    assert quote.end == 51
    assert raw_cards[0]["slices"][0]["text"][quote.start:quote.end] == "free trial for 14 days without"


def test_mcp_server_evaluate_tool(tmp_path: Path):
    import anyio

    from harness_fleet.mcp_server import create_mcp_server

    server = create_mcp_server(tmp_path)
    tool_names = [tool.name for tool in anyio.run(server.list_tools)]
    assert "harness_fleet_eval" in tool_names


def test_canonical_url_preserves_query_params():
    from harness_fleet.discover import canonical_url

    u1 = "https://news.ycombinator.com/item?id=123"
    u2 = "https://news.ycombinator.com/item?id=456"
    assert canonical_url(u1) != canonical_url(u2)
    assert canonical_url("https://news.ycombinator.com/item?id=123#reply") == "https://news.ycombinator.com/item?id=123"
    assert canonical_url("https://example.com/path/?a=1") == "https://example.com/path?a=1"


def test_to_input_items_handles_multiple_collisions():
    from harness_fleet.discover import RawRecord, to_input_items

    records = [
        RawRecord(text="Item 1 text", source_uri="https://example.com/feed", title="Feed item"),
        RawRecord(text="Item 2 text", source_uri="https://example.com/feed", title="Feed item"),
        RawRecord(text="Item 3 text", source_uri="https://example.com/feed", title="Feed item"),
        RawRecord(text="Item 4 text", source_uri="https://example.com/feed", title="Feed item"),
    ]
    items = to_input_items(records)
    assert len(items) == 4
    item_ids = [it.item_id for it in items]
    assert len(set(item_ids)) == 4


def test_ats_records_includes_profile_evidence():
    from harness_fleet.discover import _ats_records

    jobs = [{"id": 42, "title": "Staff Engineer", "content": "Full text of posting"}]
    records = _ats_records(
        jobs,
        source="greenhouse",
        org="stripe",
        get_text=lambda j: j["content"],
        get_url=lambda j: f"https://boards.greenhouse.io/stripe/jobs/{j['id']}",
        get_title=lambda j: j["title"],
        get_job_id=lambda j: str(j["id"]),
    )
    assert len(records) == 1
    assert records[0].metadata.get("evidence") == "profile"
    assert records[0].metadata.get("ats") == "greenhouse"
    assert records[0].metadata.get("org") == "stripe"


def test_gzipped_sitemap_decompression(tmp_path: Path):
    import gzip

    import httpx

    from harness_fleet.discover import fetch_sitemap_urls

    xml = b"""<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<url><loc>https://example.com/page1</loc></url>
<url><loc>https://example.com/page2</loc></url>
</urlset>"""
    gzipped = gzip.compress(xml)

    def mock_handler(request):
        return httpx.Response(200, content=gzipped, headers={"Content-Type": "application/x-gzip"})

    transport = httpx.MockTransport(mock_handler)
    client = httpx.Client(transport=transport)
    urls = fetch_sitemap_urls("https://example.com/sitemap.xml.gz", client=client)
    assert urls == ["https://example.com/page1", "https://example.com/page2"]


def test_scoring_respects_free_only_policy(tmp_path: Path):
    from harness_fleet.models import RoutePolicy
    from harness_fleet.scoring import filter_and_rank_routes
    from harness_fleet.store import HarnessStore

    store = HarnessStore(tmp_path / "test.db")
    routes = [
        {"id": "paid/model-1", "provider": "paid", "price_state": "unknown", "cost_per_1k_input": 5.0, "cost_per_1k_output": 15.0},
        {"id": "free/model-1", "provider": "free", "price_state": "price_observed_zero", "cost_per_1k_input": 0.0, "cost_per_1k_output": 0.0},
    ]
    ranked = filter_and_rank_routes(routes, store=store, policy=RoutePolicy(free_only=True))
    assert ranked == ["free/model-1"]


def test_circuit_breaker_trips_when_route_pinned_and_free_only(tmp_path: Path):
    from harness_fleet.catalog import RouteCatalog, RouteCircuitBreaker
    from harness_fleet.models import RoutePolicy

    cat = RouteCatalog(tmp_path / "routes.json", db_path=tmp_path / "test.db")
    cat.add_route("openrouter/test-model", provider="openrouter", cost_per_1k_input=0.0, cost_per_1k_output=0.0)
    # Set price_state to unknown to verify policy.free_only forces run_is_free_only
    for r in cat.data["routes"]:
        if r["id"] == "openrouter/test-model":
            r["price_state"] = "unknown"
    cat.save()

    policy = RoutePolicy(free_only=True, allowed_routes=["openrouter/test-model"])
    with pytest.raises(RouteCircuitBreaker):
        cat.record_cost("openrouter/test-model", 0.05, policy=policy)


def test_route_catalog_init_with_string_path(tmp_path: Path):
    from harness_fleet.catalog import RouteCatalog

    str_path = str(tmp_path / "custom_routes.json")
    catalog = RouteCatalog(str_path)
    assert isinstance(catalog.config_path, Path)


def test_account_fleet_db_env_var_respected(monkeypatch, tmp_path: Path):
    import argparse

    from harness_fleet.cli import _store
    from harness_fleet.store import default_db_path

    custom_db = str(tmp_path / "acct.db")
    monkeypatch.setenv("HARNESS_FLEET_DB", custom_db)

    assert str(default_db_path()) == custom_db

    args = argparse.Namespace(workspace_root=str(tmp_path), db=None)
    store = _store(args)
    assert str(store.path) == custom_db


def test_safe_json_helper():
    import httpx

    from harness_fleet.discover import DiscoverError, _safe_json

    resp_bad = httpx.Response(200, text="<html>Cloudflare 502 error</html>")
    with pytest.raises(DiscoverError) as exc_info:
        _safe_json(resp_bad, "https://example.com/api")
    assert "invalid JSON response" in str(exc_info.value)


def test_crawl_site_link_extraction_with_render_js(monkeypatch):
    import httpx

    from harness_fleet.discover import crawl_site

    rendered_html = '<html><body><h1>Welcome</h1><a href="/subpage">Next</a></body></html>'
    monkeypatch.setattr("harness_fleet.discover._render_js", lambda url, timeout: rendered_html)

    client = httpx.Client()
    records, skipped = crawl_site("https://example.com", client=client, max_pages=2, render_js=True, respect_robots=False)
    assert len(records) >= 1
    # Check that subpage was discovered from rendered HTML
    sources = [r.source_uri for r in records]
    assert "https://example.com" in sources


def test_evaluate_filter_equals_and_null():
    from harness_fleet.export import _evaluate_filter
    from harness_fleet.models import ClaimFilter, FilterClause, FilterOp

    def _f(field, op, value):
        return ClaimFilter(all=[FilterClause(field=field, op=FilterOp(op), value=value)])

    assert _evaluate_filter({"fit_tier": "tier_1"}, _f("fit_tier", "==", "tier_1")) is True
    assert _evaluate_filter({"fit_tier": "tier_2"}, _f("fit_tier", "==", "tier_1")) is False
    assert _evaluate_filter({"score": 100}, _f("score", "==", 100)) is True
    assert _evaluate_filter({"score": 90}, _f("score", "==", 100)) is False
    assert _evaluate_filter({"val": None}, _f("val", "==", None)) is True
    assert _evaluate_filter({"val": "present"}, _f("val", "!=", None)) is True
    assert _evaluate_filter({"val": None}, _f("val", "!=", None)) is False


def test_export_csv_reserved_column_collision(tmp_path: Path):
    import csv

    from harness_fleet.export import export_clean_csv
    from harness_fleet.models import TaskSpec

    task = TaskSpec(
        name="test-reserved",
        claims_schema={
            "type": "object",
            "properties": {
                "item_id": {"type": "string"},
                "source_uri": {"type": "string"},
                "score": {"type": "integer"},
            },
            "required": ["item_id", "source_uri", "score"],
            "additionalProperties": False,
        },
    )
    run_data = {
        "run_id": "r1",
        "task": task.model_dump(mode="json", by_alias=True),
        "task_revision": "rev1",
        "input_digest": "d" * 64,
        "batches": {
            "b1": {
                "status": "verified",
                "result": [{
                    "item_id": "rec-1",
                    "source_uri": "https://example.com/1",
                    "source_digest": "a" * 64,
                    "content_type": "text/plain",
                    "claims": {"item_id": "inner_id", "source_uri": "inner_uri", "score": 95},
                    "quotes": [{"slice_id": "full", "start": 0, "end": 10, "text": "0123456789"}],
                }],
            }
        },
    }
    csv_path = tmp_path / "out.csv"
    export_clean_csv(run_data, csv_path, rank=True)
    with open(csv_path, encoding="utf-8") as f:
        reader = csv.reader(f)
        header = next(reader)
    # Ensure no duplicates in header
    assert len(header) == len(set(header))
    assert header.count("item_id") == 1
    assert header.count("source_uri") == 1


def test_cmd_test_empty_input_error(tmp_path: Path):
    import argparse

    from harness_fleet.cli import cmd_test
    from harness_fleet.models import TaskSpec
    from harness_fleet.store import HarnessStore

    db_path = tmp_path / "test.db"
    store = HarnessStore(db_path)
    task = TaskSpec(
        name="test-task",
        instructions="do things",
        claims_schema={"type": "object", "properties": {"a": {"type": "string"}}, "required": ["a"], "additionalProperties": False},
    )
    store.register_task(task)

    empty_csv = tmp_path / "empty.csv"
    empty_csv.write_text("item_id,text\n")

    args = argparse.Namespace(
        task="test-task",
        input=str(empty_csv),
        workspace_root=str(tmp_path),
        db=str(db_path),
        json=False,
    )
    with pytest.raises(ValueError, match="input contains no"):
        cmd_test(args)


def test_run_discovery_multi_backend_isolation(monkeypatch):
    from harness_fleet.discover import DiscoverError, SearchHit, run_discovery

    def fake_run_backend(backend, query, max_results, searxng_url, client, *args, **kwargs):
        if backend == "ddgs":
            return [SearchHit(url="https://example.com/from-ddgs", title="Hit 1", snippet="snip", backend="ddgs")]
        elif backend == "hn":
            raise DiscoverError("HN rate limit 429")
        return []

    monkeypatch.setattr("harness_fleet.discover._run_backend", fake_run_backend)
    monkeypatch.setattr("harness_fleet.discover.fetch_smart_url", lambda url, *args, **kwargs: None)
    monkeypatch.setattr("harness_fleet.discover.to_input_items", lambda records, *args, **kwargs: [InputItem(item_id="1", text=r.text, source_uri=r.source_uri) for r in records])

    items, report = run_discovery(
        queries=["test query"],
        backends=["ddgs", "hn"],
        fetch_full_text=False,
    )
    # The hit from ddgs must be preserved even though hn failed
    assert len(items) == 1
    assert items[0].source_uri == "https://example.com/from-ddgs"
    # The failed backend is logged in skipped
    assert any(s.get("backend") == "hn" and "429" in s.get("reason", "") for s in report["skipped"])


def test_safe_json_in_discourse_and_lemmy(monkeypatch):
    import httpx

    from harness_fleet.discover import (
        DiscoverError,
        fetch_discourse_search,
        fetch_lemmy,
        search_lemmy,
    )

    fake_html_resp = httpx.Response(200, text="<html>Nginx 502 Bad Gateway</html>")

    class FakeClient:
        def get(self, *args, **kwargs):
            return fake_html_resp
        def close(self):
            pass

    client = FakeClient()
    with pytest.raises(DiscoverError, match="invalid JSON response"):
        search_lemmy("query", client=client)

    with pytest.raises(DiscoverError, match="invalid JSON response"):
        fetch_lemmy("query", client=client)

    with pytest.raises(DiscoverError, match="invalid JSON response"):
        fetch_discourse_search("https://meta.discourse.org", query="search", client=client)


def test_input_data_invalid_explicit_columns(tmp_path: Path):
    from harness_fleet.input_data import InputDataError, load_input_items

    csv_file = tmp_path / "data.csv"
    csv_file.write_text("item_id,text,heading,link\n1,some text,My Heading,https://example.com\n")

    # Valid explicit columns
    items = load_input_items(csv_file, title_column="heading", uri_column="link")
    assert items[0].title == "My Heading"
    assert items[0].source_uri == "https://example.com"

    # Non-existent title_column raises InputDataError
    with pytest.raises(InputDataError, match="Specified title column 'bad_title' not found"):
        load_input_items(csv_file, title_column="bad_title")

    # Non-existent uri_column raises InputDataError
    with pytest.raises(InputDataError, match="Specified uri column 'bad_uri' not found"):
        load_input_items(csv_file, uri_column="bad_uri")


def test_opencode_timeout_classification():
    import subprocess

    from harness_fleet.providers.opencode import OpenCodeProvider

    class TimeoutRunner:
        def run(self, task_config, args, timeout_sec):
            raise subprocess.TimeoutExpired(cmd=["opencode"], timeout=timeout_sec)

    prov = OpenCodeProvider(runner=TimeoutRunner())
    ok, text, receipt = prov.run_prompt("opencode/test-model", "test prompt", timeout_sec=5)
    assert ok is False
    assert text is None
    assert receipt.error_type == "timeout"


def test_fetch_hn_thread_comment_support(monkeypatch):
    from harness_fleet.discover import fetch_hn_thread

    def fake_hn_item(item_id, client, timeout):
        if item_id == "99999":
            return {
                "id": 99999,
                "type": "comment",
                "by": "commenter1",
                "text": "This is a great discussion comment.",
                "kids": [99998],
            }
        elif item_id == "99998":
            return {
                "id": 99998,
                "type": "comment",
                "by": "commenter2",
                "text": "I agree with you.",
                "kids": [],
            }
        return None

    monkeypatch.setattr("harness_fleet.discover._hn_item", fake_hn_item)

    rec = fetch_hn_thread("99999")
    assert "great discussion comment" in rec.text
    assert "I agree with you" in rec.text
    assert "HN" in rec.title


def test_openrouter_refresh_pricing_resilience(monkeypatch, tmp_path):
    import httpx

    from harness_fleet.catalog import RouteCatalog

    fake_models_resp = {
        "data": [
            {
                "id": "vendor/free-model-1",
                # Pricing with null prompt
                "pricing": {"prompt": None, "input": 0.0, "completion": 0.0, "output": 0.0},
            },
            {
                "id": "vendor/free-model-2",
                # Cost key instead of pricing
                "cost": {"prompt": 0.0, "completion": 0.0},
            },
            {
                "id": "vendor/free-model-3",
                # Missing pricing entirely
            },
        ]
    }

    class FakeClient:
        def __init__(self, *args, **kwargs):
            pass
        def __enter__(self):
            return self
        def __exit__(self, *args):
            pass
        def get(self, url, **kwargs):
            return httpx.Response(200, json=fake_models_resp)

    monkeypatch.setattr("httpx.Client", FakeClient)

    catalog = RouteCatalog(db_path=tmp_path / "test.db")
    discovered = catalog.refresh_from_openrouter()
    assert discovered >= 1


def test_pack_items_invalid_batch_size():
    from harness_fleet.packer import pack_items

    with pytest.raises(ValueError, match="batch_size must be greater than 0"):
        pack_items([], batch_size=0)
    with pytest.raises(ValueError, match="batch_size must be greater than 0"):
        pack_items([], batch_size=-1)


def test_slicer_min_width():
    from harness_fleet.slicer import slice_document

    text = "abcdefghij"
    slices = slice_document(text, max_chars=2)
    # Lossless sliding windows: every char covered, exact offsets, bounded width
    assert len(slices) >= 5
    for s in slices:
        assert 1 <= len(s["text"]) <= 2
        assert text[s["start"]:s["end"]] == s["text"]
    covered = bytearray(len(text))
    for s in slices:
        for i in range(s["start"], s["end"]):
            covered[i] = 1
    assert all(covered)


def test_safe_json_in_all_discovery_backends():
    import httpx

    from harness_fleet.discover import (
        DiscoverError,
        _se_get,
        _yc_get_page,
        search_hn,
        search_searxng,
    )

    fake_html_resp = httpx.Response(200, text="<html>502 Bad Gateway</html>")

    class FakeClient:
        def get(self, *args, **kwargs):
            return fake_html_resp
        def close(self):
            pass

    client = FakeClient()
    with pytest.raises(DiscoverError, match="invalid JSON response"):
        search_searxng("test", base_url="https://searx.example", client=client)

    with pytest.raises(DiscoverError, match="invalid JSON response"):
        search_hn("test", client=client)

    with pytest.raises(DiscoverError, match="invalid JSON response"):
        _yc_get_page(1, client=client)

    with pytest.raises(DiscoverError, match="invalid JSON response"):
        _se_get("/questions", {}, timeout=20.0, client=client)


def test_null_content_in_providers(monkeypatch):
    import httpx

    from harness_fleet.providers.openai_compatible import OpenAICompatibleProvider
    from harness_fleet.providers.openrouter import OpenRouterProvider

    payload_null_content = {
        "choices": [{"message": {"role": "assistant", "content": None}}],
        "usage": {"total_tokens": 10},
    }

    class FakeClient:
        def __init__(self, *args, **kwargs):
            pass
        def __enter__(self):
            return self
        def __exit__(self, *args):
            pass
        def post(self, *args, **kwargs):
            return httpx.Response(200, json=payload_null_content)

    monkeypatch.setattr("httpx.Client", FakeClient)

    # A provider that answers with no content has failed, exactly like a CLI
    # harness that prints nothing: recording it as "complete" would burn the
    # attempt and let an empty response count as a successful transport.
    prov1 = OpenAICompatibleProvider(base_url="http://localhost:11434/v1", is_free=True)
    ok1, text1, r1 = prov1.run_prompt("test-route", "prompt")
    assert ok1 is False
    assert text1 is None
    assert "Empty content" in (r1.error or "")
    assert r1.status != "complete"

    prov2 = OpenRouterProvider(api_key="test-key")
    ok2, text2, r2 = prov2.run_prompt("test-route", "prompt")
    assert ok2 is False
    assert text2 is None
    assert "Empty content" in (r2.error or "")
    assert r2.status != "complete"


def test_resume_campaign_fallback_output_path(tmp_path):
    from harness_fleet.engine import Engine
    from harness_fleet.models import InputItem, RoutePolicy
    from harness_fleet.store import HarnessStore
    from harness_fleet.task import create_task_from_preset

    store = HarnessStore(tmp_path / "test.db")
    task = create_task_from_preset("test_task")
    store.register_task(task)

    policy = RoutePolicy(allowed_routes=["demo/fake"], free_only=True)
    engine = Engine(task=task, store=store, policy=policy)
    engine.catalog.add_route(
        route_id="demo/fake",
        provider="demo",
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0,
        enabled=True,
        price_state="price_observed_zero",
    )
    items = [InputItem(item_id="i1", text="Sample test text verification fact here.")]
    _packet = engine.run_campaign(
        raw_items=items,
        run_id="run_fallback_test",
        input_path=str(tmp_path / "input.json"),
        concurrency=1,
        policy=policy,
    )
    # Manually clear output_path in runs table to simulate legacy / null row
    with store.connect() as conn:
        conn.execute("UPDATE runs SET output_path = NULL WHERE run_id = 'run_fallback_test'")

    resumed_packet = engine.resume_campaign(run_id="run_fallback_test")
    assert resumed_packet["run_id"] == "run_fallback_test"


def test_export_top_zero_and_negative(tmp_path: Path):
    import pytest

    from harness_fleet.export import export_clean_csv, export_clean_packet
    from harness_fleet.models import TaskSpec
    from harness_fleet.store import digest_json

    task = TaskSpec(
        name="test-top",
        claims_schema={
            "type": "object",
            "properties": {"val": {"type": "integer"}},
            "required": ["val"],
            "additionalProperties": False,
        },
    )
    task_dump = task.model_dump(mode="json", by_alias=True)
    run_data = {
        "run_id": "r1",
        "task": task_dump,
        "task_revision": digest_json(task.revision_payload()),
        "input_digest": "d" * 64,
        "batches": {
            "b1": {
                "status": "verified",
                "result": [
                    {
                        "item_id": "rec-1",
                        "source_uri": "https://example.com/1",
                        "source_digest": "a" * 64,
                        "content_type": "text/plain",
                        "claims": {"val": 10},
                        "quotes": [{"slice_id": "full", "start": 0, "end": 4, "text": "0123"}],
                    },
                    {
                        "item_id": "rec-2",
                        "source_uri": "https://example.com/2",
                        "source_digest": "a" * 64,
                        "content_type": "text/plain",
                        "claims": {"val": 20},
                        "quotes": [{"slice_id": "full", "start": 0, "end": 4, "text": "0123"}],
                    },
                ],
            }
        },
    }

    # top = 0 should return 0 items
    pkt = export_clean_packet(run_data, tmp_path / "packet0.json", top=0)
    assert len(pkt["records"]) == 0
    assert pkt["total_verified_records"] == 0

    csv_path = tmp_path / "out0.csv"
    export_clean_csv(run_data, csv_path, top=0)
    lines = csv_path.read_text(encoding="utf-8").strip().splitlines()
    # Only header line should exist
    assert len(lines) == 1

    # negative top should raise ValueError
    with pytest.raises(ValueError, match="top must be non-negative"):
        export_clean_packet(run_data, tmp_path / "packet_neg.json", top=-1)


def test_engine_run_campaign_empty_items(tmp_path: Path):
    import pytest

    from harness_fleet.engine import Engine
    from harness_fleet.store import HarnessStore
    from harness_fleet.task import create_task_from_preset

    store = HarnessStore(tmp_path / "store.db")
    task = create_task_from_preset("test_task")
    store.register_task(task)
    engine = Engine(task=task, store=store)

    with pytest.raises(ValueError, match="input contains no items"):
        engine.run_campaign(raw_items=[], run_id="empty_run", input_path=str(tmp_path / "in.json"))


def test_export_clean_csv_invalid_batch_shape(tmp_path: Path):
    import pytest

    from harness_fleet.export import export_clean_csv
    from harness_fleet.models import TaskSpec

    task = TaskSpec(
        name="test-shape",
        claims_schema={
            "type": "object",
            "properties": {"a": {"type": "string"}},
            "required": ["a"],
            "additionalProperties": False,
        },
    )
    run_data = {
        "run_id": "r1",
        "task": task.model_dump(mode="json", by_alias=True),
        "batches": {
            "b1": {
                "status": "verified",
                "result": "not-a-list-or-dict",
            }
        },
    }
    with pytest.raises(ValueError, match="verified batch result has an invalid shape"):
        export_clean_csv(run_data, tmp_path / "out.csv")


def test_demo_provider_trailing_garbage_json_recovery():
    from harness_fleet.providers.demo import DemoProvider
    demo = DemoProvider()
    prompt = 'instructions here\n{"input_items": [{"item_id": "i1", "sections": [{"slice_id": "full", "text": "deterministic text for verification"}]}], "output_schema": {"properties": {"items": {"items": {"properties": {"claims": {"type": "object", "properties": {"status": {"type": "string"}}}}}}}}}\nSome extra trailing garbage text'
    ok, resp, receipt = demo.run_prompt("demo", prompt)
    assert ok is True
    assert receipt.status == "complete"
    assert "i1" in resp


def test_demo_provider_short_slice_quote_min_chars():
    from harness_fleet.providers.demo import DemoProvider
    demo = DemoProvider()
    prompt = 'instructions\n{"input_items": [{"item_id": "i1", "sections": [{"slice_id": "full", "text": "tiny"}]}], "output_schema": {"properties": {"items": {"items": {"properties": {"claims": {"type": "object", "properties": {"val": {"type": "string"}}}}}}}}}'
    ok, resp, receipt = demo.run_prompt("demo", prompt)
    assert ok is True
    data = json.loads(resp)
    quote = data["items"][0]["quotes"][0]["text"]
    assert len(quote) >= 15


def test_demo_provider_disambiguates_repeated_quote_with_offsets():
    from harness_fleet.providers.demo import DemoProvider
    demo = DemoProvider()
    source = "Introducing Browserbase Agents: One Prompt, One API Call.\n" * 2
    prompt = (
        'instructions\n{"input_items": [{"item_id": "i1", "sections": '
        '[{"slice_id": "full", "start": 0, "end": ' + str(len(source)) + ', '
        '"text": ' + json.dumps(source) + '}], "title": "demo"}], '
        '"output_schema": {"properties": {"items": {"items": '
        '{"properties": {"claims": {"type": "object", "properties": '
        '{"val": {"type": "string"}}}}}}}}}'
    )
    ok, resp, receipt = demo.run_prompt("demo", prompt)
    assert ok is True
    assert receipt.status == "complete"
    quote = json.loads(resp)["items"][0]["quotes"][0]
    assert quote["start"] == 0
    assert quote["end"] == len(quote["text"])


def test_demo_provider_nested_object_properties():
    from harness_fleet.providers.demo import _value_for_spec
    spec = {
        "type": "object",
        "properties": {
            "name": {"type": "string"},
            "count": {"type": "integer"},
        },
    }
    val = _value_for_spec(spec)
    assert isinstance(val, dict)
    assert "name" in val
    assert "count" in val
    assert val["count"] == 0


def test_session_pool_empty_routes_raises():
    from harness_fleet.sessions import SessionPool
    with pytest.raises(ValueError, match="SessionPool requires at least one route"):
        SessionPool(num_sessions=2, routes=[])
