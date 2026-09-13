import csv
import json
import re
import sys
import types
from argparse import Namespace

import pytest

from free_fleet import cli, discover
from free_fleet.discover import (
    DiscoverError,
    RawRecord,
    SearchHit,
    canonical_url,
    extract_text,
    fetch_ashby_org,
    fetch_greenhouse_board,
    fetch_lever_org,
    fetch_text,
    robots_allowed,
    run_discovery,
    search_hn,
    search_searxng,
    slugify_id,
    to_input_items,
    web_search,
    write_items_csv,
    write_items_jsonl,
)
from free_fleet.input_data import load_input_items
from free_fleet.models import ID_PATTERN


@pytest.fixture(autouse=True)
def clear_robots_cache():
    discover._robots_cache.clear()
    yield
    discover._robots_cache.clear()


class FakeResponse:
    def __init__(self, *, status=200, headers=None, content=b"", json_data=None, url="https://example.com/"):
        self.status_code = status
        self.headers = headers or {}
        self.content = content
        self._json = json_data
        self.url = url

    @property
    def text(self):
        return self.content.decode("utf-8", errors="replace")

    def json(self):
        if self._json is None:
            raise ValueError("no json")
        return self._json


class FakeClient:
    routes: dict = {}

    def __init__(self, *args, **kwargs):
        pass

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return None

    def close(self):
        pass

    def get(self, url, params=None, timeout=None):
        key = str(url)
        if key in self.routes:
            resp = self.routes[key]
            if isinstance(resp, Exception):
                raise resp
            return resp
        return FakeResponse(status=404, content=b"not found")


@pytest.fixture()
def fake_http(monkeypatch):
    FakeClient.routes = {}
    monkeypatch.setattr("free_fleet.discover.httpx.Client", FakeClient)
    return FakeClient


# --- IDs -------------------------------------------------------------------

def test_slugify_produces_valid_ids():
    slug = slugify_id("https://jobs.ashbyhq.com/stripe/Staff Engineer: billing!")
    assert re.match(ID_PATTERN + r"\Z", slug)
    assert len(slugify_id("x" * 500)) <= 128
    assert slugify_id("") == "item"


def test_canonical_url_dedupes():
    assert canonical_url("https://Example.com/path/") == canonical_url("https://example.com/path")


# --- Extraction ------------------------------------------------------------

HTML_DOC = """<html><head><title>Scaling Postgres</title></head><body>
<script>var x = 1;</script><style>.a{}</style>
<article><h1>Scaling Postgres</h1><p>We hit latency limits at 50k QPS on our Postgres cluster.</p></article>
</body></html>"""


def test_fallback_extraction_skips_boilerplate():
    text = extract_text("<p>only fallback here</p>")
    assert "only fallback here" in text


def test_extract_text_prefers_trafilatura(monkeypatch):
    fake = types.ModuleType("trafilatura")
    fake.extract = lambda html, **kw: "TRAFILATURA MAIN"
    monkeypatch.setitem(sys.modules, "trafilatura", fake)
    assert extract_text(HTML_DOC) == "TRAFILATURA MAIN"


def test_extract_text_falls_back_to_readability(monkeypatch):
    monkeypatch.setitem(sys.modules, "trafilatura", None)
    fake_r = types.ModuleType("readability")
    doc_cls = type("Document", (), {"__init__": lambda self, h: None, "summary": lambda self: "<p>READABILITY MAIN</p>"})
    fake_r.Document = doc_cls
    monkeypatch.setitem(sys.modules, "readability", fake_r)
    assert extract_text(HTML_DOC) == "READABILITY MAIN"


def test_extract_text_stdlib_last_resort(monkeypatch):
    monkeypatch.setitem(sys.modules, "trafilatura", None)
    monkeypatch.setitem(sys.modules, "readability", None)
    # Force ImportError on `from readability import Document`
    import builtins

    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name in ("trafilatura", "readability"):
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)
    text = extract_text(HTML_DOC)
    assert "latency limits at 50k QPS" in text
    assert "var x = 1" not in text


# --- Fetch -----------------------------------------------------------------

def test_fetch_text_parses_html(fake_http):
    FakeClient.routes["https://example.com/blog"] = FakeResponse(
        headers={"content-type": "text/html"}, content=HTML_DOC.encode(), url="https://example.com/blog",
    )
    rec = fetch_text("https://example.com/blog", respect_robots=False)
    assert "latency limits at 50k QPS" in rec.text
    assert rec.title == "Scaling Postgres"
    assert rec.source_uri == "https://example.com/blog"


def test_fetch_text_rejects_non_html(fake_http):
    FakeClient.routes["https://example.com/app.exe"] = FakeResponse(
        headers={"content-type": "application/octet-stream"}, content=b"\x00\x01", url="https://example.com/app.exe",
    )
    with pytest.raises(DiscoverError, match="content-type"):
        fetch_text("https://example.com/app.exe", respect_robots=False)


def test_fetch_text_raises_on_http_error(fake_http):
    with pytest.raises(DiscoverError, match="HTTP 404"):
        fetch_text("https://example.com/missing", respect_robots=False)


def test_robots_txt_blocks_and_allows(fake_http):
    FakeClient.routes["https://example.com/robots.txt"] = FakeResponse(
        headers={"content-type": "text/plain"}, content=b"User-agent: *\nDisallow: /private\n",
    )
    client = FakeClient()
    assert robots_allowed("https://example.com/public/page", client) is True
    assert robots_allowed("https://example.com/private/page", client) is False
    with pytest.raises(DiscoverError, match="robots.txt"):
        fetch_text("https://example.com/private/page")


def test_robots_fail_open(fake_http):
    FakeClient.routes["https://down.example/robots.txt"] = ConnectionError("dns")
    assert robots_allowed("https://down.example/any", FakeClient()) is True


# --- Search backends --------------------------------------------------------

def test_search_hn_maps_hits(fake_http):
    FakeClient.routes[discover.HN_API] = FakeResponse(json_data={"hits": [
        {"objectID": "123", "title": "Who is hiring?", "url": "", "story_text": "Acme hires Kafka engineers"},
    ]})
    hits = search_hn("hiring")
    assert hits[0].url == "https://news.ycombinator.com/item?id=123"
    assert hits[0].backend == "hn"


def test_search_searxng_parses_results(fake_http):
    FakeClient.routes["http://localhost:8888/search"] = FakeResponse(json_data={"results": [
        {"url": "https://a.example/x", "title": "A", "content": "snippet a"},
        {"url": "not-a-url", "title": "B", "content": "skip me"},
    ]})
    hits = search_searxng("q", base_url="http://localhost:8888", max_results=1)
    assert [h.url for h in hits] == ["https://a.example/x"]
    assert hits[0].backend == "searxng"


def test_search_ddgs_missing_gives_install_hint(monkeypatch):
    import builtins

    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "ddgs":
            raise ImportError("ddgs")
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)
    with pytest.raises(DiscoverError, match="account-fleet\\[discover\\]"):
        discover.search_ddgs("q")


def test_web_search_rejects_unknown_backend():
    with pytest.raises(DiscoverError, match="unknown search backend"):
        web_search("q", backends=["google"])


def test_web_search_dedupes_across_backends(monkeypatch):
    monkeypatch.setattr(discover, "search_ddgs", lambda q, max_results=10: [
        SearchHit(url="https://Example.com/a/", title="A", snippet="s", backend="ddgs"),
    ])
    monkeypatch.setattr(discover, "search_hn", lambda q, max_results=10, client=None: [
        SearchHit(url="https://example.com/a", title="A2", snippet="s2", backend="hn"),
        SearchHit(url="https://b.example/", title="B", snippet="s3", backend="hn"),
    ])
    hits = web_search("q", backends=["ddgs", "hn"])
    assert [h.url for h in hits] == ["https://Example.com/a/", "https://b.example/"]


# --- ATS --------------------------------------------------------------------

def test_greenhouse_board_parses_content(fake_http):
    FakeClient.routes["https://boards-api.greenhouse.io/v1/boards/stripe/jobs?content=true"] = FakeResponse(
        json_data={"jobs": [{"id": 8172487, "title": "Eng", "absolute_url": "https://stripe.com/jobs/x",
                             "content": "<p>migrating legacy billing to Kafka</p>",
                             "location": {"name": "New York, NY"},
                             "timezone": "America/New_York"}]}
    )
    recs = fetch_greenhouse_board("stripe")
    assert len(recs) == 1
    assert "migrating legacy billing to Kafka" in recs[0].text
    assert recs[0].metadata["ats"] == "greenhouse"
    assert recs[0].metadata["location"] == "New York, NY"
    assert recs[0].metadata["timezone"] == "America/New_York"


def test_greenhouse_unknown_board(fake_http):
    with pytest.raises(DiscoverError, match="unknown Greenhouse board"):
        fetch_greenhouse_board("nope")


def test_ashby_prefers_plain_description(fake_http):
    FakeClient.routes["https://api.ashbyhq.com/posting-api/job-board/linear"] = FakeResponse(
        json_data={"jobs": [{"id": "abc123", "title": "Eng", "jobUrl": "https://jobs.ashbyhq.com/linear/x",
                             "isListed": True, "descriptionPlain": "plain verbatim",
                             "descriptionHtml": "<p>html version</p>"},
                            {"id": "hidden", "title": "X", "jobUrl": "", "isListed": False,
                             "descriptionPlain": "unlisted"}]}
    )
    recs = fetch_ashby_org("linear")
    assert len(recs) == 1
    assert recs[0].text == "plain verbatim"


def test_ats_ids_keep_full_provider_ids_and_dedupe_duplicate_rows(fake_http):
    FakeClient.routes["https://api.ashbyhq.com/posting-api/job-board/linear"] = FakeResponse(
        json_data={"jobs": [
            {"id": "abcdefgh-one", "title": "One", "jobUrl": "https://jobs.ashbyhq.com/linear/one",
             "isListed": True, "descriptionPlain": "first"},
            {"id": "abcdefgh-two", "title": "Two", "jobUrl": "https://jobs.ashbyhq.com/linear/two",
             "isListed": True, "descriptionPlain": "second"},
            {"id": "abcdefgh-one", "title": "One duplicate", "jobUrl": "https://jobs.ashbyhq.com/linear/duplicate",
             "isListed": True, "descriptionPlain": "third"},
        ]}
    )

    recs = fetch_ashby_org("linear")

    assert len(recs) == 3
    assert len({record.item_id for record in recs}) == 3
    assert "abcdefgh-one" in recs[0].item_id
    assert "abcdefgh-two" in recs[1].item_id


def test_lever_404_names_migration(fake_http):
    with pytest.raises(DiscoverError, match="migrated"):
        fetch_lever_org("ghost-org")


def test_lever_uses_description_instead_of_title_as_source_text(fake_http):
    FakeClient.routes["https://api.lever.co/v0/postings/acme?mode=json"] = FakeResponse(
        json_data=[{"id": "job-1", "text": "Senior Platform Engineer",
                    "description": "Build Kafka pipelines and distributed systems.",
                    "hostedUrl": "https://jobs.lever.co/acme/job-1",
                    "categories": {"location": "Remote"}}]
    )
    recs = fetch_lever_org("acme")
    assert recs[0].title == "Senior Platform Engineer"
    assert recs[0].text == "Build Kafka pipelines and distributed systems."


# --- Orchestration ----------------------------------------------------------

def test_to_input_items_dedupes_and_validates():
    items = to_input_items([
        RawRecord(text="alpha", source_uri="https://a.example/1", title="A"),
        RawRecord(text="   ", source_uri="https://a.example/2"),
        RawRecord(text="alpha again", source_uri="https://a.example/1"),
    ])
    assert len(items) == 2
    assert all(re.match(ID_PATTERN + r"\Z", i.item_id) for i in items)


def test_write_csv_round_trips_through_loader(tmp_path):
    items = to_input_items([RawRecord(text="verbatim quote here", source_uri="https://a.example/1", title="T")])
    out = write_items_csv(items, tmp_path / "accounts.csv")
    loaded = load_input_items(out)
    assert loaded[0].text == "verbatim quote here"
    assert loaded[0].source_uri == "https://a.example/1"
    with open(out, newline="", encoding="utf-8") as f:
        assert next(csv.DictReader(f)).keys() >= {"item_id", "text", "source_uri"}


def test_write_jsonl_round_trips(tmp_path):
    items = to_input_items([RawRecord(text="t", source_uri="https://a.example/1", metadata={"ats": "x"})])
    out = write_items_jsonl(items, tmp_path / "a.jsonl")
    row = json.loads(out.read_text().splitlines()[0])
    assert row["metadata"] == {"ats": "x"}


def test_run_discovery_snippets_only(monkeypatch):
    monkeypatch.setattr(discover, "search_ddgs", lambda q, max_results=10: [
        SearchHit(url="https://a.example/1", title="A", snippet="snippet alpha", backend="ddgs"),
    ])
    monkeypatch.setattr(discover, "search_hn", lambda q, max_results=10, client=None: [])
    items, report = run_discovery(["q"], backends=["ddgs", "hn"], fetch_full_text=False, delay=0)
    assert len(items) == 1 and items[0].text == "snippet alpha"
    assert report["hits"] == 1


def test_run_discovery_fetch_failures_are_skipped(monkeypatch):
    monkeypatch.setattr(discover, "search_ddgs", lambda q, max_results=10: [
        SearchHit(url="https://bad.example/", title="B", snippet="", backend="ddgs"),
    ])
    monkeypatch.setattr(discover, "fetch_text", lambda url, **kw: (_ for _ in ()).throw(DiscoverError("boom")))
    items, report = run_discovery(["q"], backends=["ddgs"], delay=0)
    assert items == [] and report["skipped"][0]["reason"] == "boom"


# --- CLI ---------------------------------------------------------------------

def test_cli_discover_writes_file(tmp_path, monkeypatch, capsys):
    from free_fleet.discover import to_input_items as _to_items

    def fake_run(**kwargs):
        assert kwargs["queries"] == ["kafka hiring"]
        items = _to_items([RawRecord(text="t", source_uri="https://a.example/")])
        return items, {"queries": ["kafka hiring"], "hits": 1, "items": 1, "skipped": []}

    monkeypatch.setattr(cli, "run_discovery", fake_run)
    out = tmp_path / "out.csv"
    cli.cmd_discover(Namespace(query=["kafka hiring"], backend=None, searxng_url=None, max_results=5,
                               snippets_only=False, delay=0, timeout=5, max_chars=None,
                               ignore_robots=False, output=str(out), format="csv", json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["items"] == 1 and out.is_file()


def test_cli_fetch_needs_a_source(tmp_path):
    with pytest.raises(DiscoverError, match="--url"):
        cli.cmd_fetch(Namespace(url=None, url_file=None, greenhouse_board=None, ashby_org=None,
                                lever_org=None, max_jobs=None, delay=0, timeout=5, max_chars=None,
                                ignore_robots=True, output=str(tmp_path / "f.csv"), format="csv", json=True))


def test_cli_fetch_ats_board(tmp_path, fake_http, capsys):
    FakeClient.routes["https://api.ashbyhq.com/posting-api/job-board/linear"] = FakeResponse(
        json_data={"jobs": [{"id": "abc", "title": "E", "jobUrl": "https://jobs.ashbyhq.com/linear/x",
                             "isListed": True, "descriptionPlain": "plain"}]}
    )
    out = tmp_path / "f.csv"
    cli.cmd_fetch(Namespace(url=None, url_file=None, greenhouse_board=None, ashby_org="linear",
                            lever_org=None, max_jobs=None, delay=0, timeout=5, max_chars=None,
                            ignore_robots=True, output=str(out), format="csv", json=True))
    assert json.loads(capsys.readouterr().out)["items"] == 1


def test_parser_registers_new_commands():
    args = cli.build_parser().parse_args(["discover", "--query", "kafka hiring"])
    assert args.command == "discover" and args.query == ["kafka hiring"]
    args = cli.build_parser().parse_args(["fetch", "--ashby-org", "linear"])
    assert args.command == "fetch" and args.ashby_org == "linear"


# --- Review fixes -----------------------------------------------------------

def test_ids_are_deterministic_across_processes():
    import hashlib

    items = discover.to_input_items([
        RawRecord(text="a", source_uri="https://d.example/1"),
        RawRecord(text="b", source_uri="https://d.example/2"),
    ])
    assert items[0].item_id == "d.example"
    assert items[1].item_id == f"d.example-{hashlib.sha256(b'https://d.example/2').hexdigest()[:8]}"


def test_record_id_prefers_readable_title_slug():
    rid = discover.record_id("https://news.ycombinator.com/item?id=1", "Who is hiring? (June 2026)")
    assert rid.startswith("news.ycombinator.com-Who-is-hiring")
    assert re.match(ID_PATTERN + r"\Z", rid)


def test_robots_stacked_user_agents_share_group(fake_http):
    # Real-world shape (google.com): * and Yandex lines open ONE group.
    FakeClient.routes["https://g.example/robots.txt"] = FakeResponse(content=(
        b"User-agent: *\nUser-agent: Yandex\nDisallow: /search\nAllow: /search/about\n"
        b"User-agent: OtherBot\nDisallow: /\n"
    ))
    client = FakeClient()
    assert robots_allowed("https://g.example/search?q=x", client) is False
    assert robots_allowed("https://g.example/search/about", client) is True  # longer Allow wins
    assert robots_allowed("https://g.example/other", client) is True  # OtherBot group ignored


def test_extractor_skips_chrome_and_survives_unclosed_tags():
    html = """<html><body>
<div class="site-nav"><img src=x><p>Menu link<p>More links</div>
<div id="main-content"><p>Real article about Kafka migrations.</p></div>
<div role="complementary">Related stories widget</div>
</body></html>"""
    text = extract_text(html)
    assert "Real article about Kafka migrations" in text
    assert "Menu link" not in text and "Related stories" not in text


def test_extractor_skips_chat_cta_widgets():
    html = """<html><body>
<div class="UniversalChatCtaCard"><img src="x"/><p><span>8</span> sales reps available</p>
<p>Chat now with sales</p></div>
<article><p>Real article about Kafka migrations.</p></article>
</body></html>"""
    text = extract_text(html)
    assert "Real article about Kafka migrations" in text
    assert "sales reps available" not in text and "Chat now" not in text


def test_extractor_void_endtags_keep_stack():
    html = '<html><body><div id="main"><p>Keep me<img/></p><p>More here</p></div></body></html>'
    text = extract_text(html)
    assert "Keep me" in text and "More here" in text


def test_search_ddgs_success_path(monkeypatch):
    class FakeDDGS:
        def __enter__(self): return self
        def __exit__(self, *a): return None
        def text(self, query, max_results=10):
            return [
                {"href": "https://a.example/x", "title": "A", "body": "snippet"},
                {"href": "ftp://nope", "title": "B", "body": "skipped scheme"},
            ]

    fake_mod = types.ModuleType("ddgs")
    fake_mod.DDGS = FakeDDGS
    monkeypatch.setitem(sys.modules, "ddgs", fake_mod)
    hits = discover.search_ddgs("q")
    assert [(h.url, h.backend) for h in hits] == [("https://a.example/x", "ddgs")]


def test_searxng_paginates_to_max_results(fake_http):
    seen_pages = []

    class PagingClient(FakeClient):
        def get(self, url, params=None, timeout=None):
            seen_pages.append((params or {}).get("pageno"))
            n = (params or {}).get("pageno", 1)
            rows = [{"url": f"https://p{n}-{i}.example/", "title": "t", "content": "c"} for i in range(2)]
            return FakeResponse(json_data={"results": rows})

    monkeypatch_client = PagingClient()
    hits = search_searxng("q", base_url="http://s.example", max_results=3, client=monkeypatch_client)
    assert len(hits) == 3 and seen_pages == [1, 2]


def test_fetch_honors_charset_header(fake_http):
    body = "café naïve façade".encode("windows-1252")
    FakeClient.routes["https://old.example/page"] = FakeResponse(
        headers={"content-type": "text/html; charset=windows-1252"}, content=body,
        url="https://old.example/page",
    )
    rec = fetch_text("https://old.example/page", respect_robots=False)
    assert "café" in rec.text


def test_fetch_pdf_with_fake_pypdf(monkeypatch, fake_http):
    class FakePage:
        def extract_text(self): return "PDF CONTENT HERE"

    class FakeReader:
        def __init__(self, buf): self.pages = [FakePage()]

    fake_mod = types.ModuleType("pypdf")
    fake_mod.PdfReader = FakeReader
    monkeypatch.setitem(sys.modules, "pypdf", fake_mod)
    FakeClient.routes["https://f.example/doc.pdf"] = FakeResponse(
        headers={"content-type": "application/pdf"}, content=b"%PDF-1.4 fake",
        url="https://f.example/doc.pdf",
    )
    rec = fetch_text("https://f.example/doc.pdf", respect_robots=False)
    assert rec.text == "PDF CONTENT HERE"


def test_fetch_pdf_without_parser_errors_cleanly(monkeypatch, fake_http):
    import builtins

    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name in ("pypdf", "pdfminer.high_level"):
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)
    FakeClient.routes["https://f.example/doc.pdf"] = FakeResponse(
        headers={"content-type": "application/pdf"}, content=b"%PDF-1.4 fake",
        url="https://f.example/doc.pdf",
    )
    with pytest.raises(DiscoverError, match="account-fleet\\[discover\\]"):
        fetch_text("https://f.example/doc.pdf", respect_robots=False)


def test_run_discovery_rejects_bad_backend_upfront():
    with pytest.raises(DiscoverError, match="unknown search backend"):
        run_discovery(["q"], backends=["google"])
    with pytest.raises(DiscoverError, match="searxng-url"):
        run_discovery(["q"], backends=["searxng"])


def test_cli_discover_empty_result_errors_without_file(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(cli, "run_discovery", lambda **kw: ([], {"queries": [], "hits": 0, "items": 0,
                                                              "skipped": [{"query": "q", "reason": "boom"}]}))
    out = tmp_path / "accounts.csv"
    with pytest.raises(DiscoverError, match="boom"):
        cli.cmd_discover(Namespace(query=["q"], backend=None, searxng_url=None, max_results=5,
                                   snippets_only=True, delay=0, timeout=5, max_chars=None, js=False,
                                   ignore_robots=True, output=str(out), format="csv", json=True))
    assert not out.exists()


def test_cli_fetch_url_file_missing(tmp_path):
    with pytest.raises(DiscoverError, match="cannot read --url-file"):
        cli.cmd_fetch(Namespace(url=None, url_file=str(tmp_path / "nope.txt"), greenhouse_board=None,
                                ashby_org=None, lever_org=None, max_jobs=None, delay=0, timeout=5,
                                max_chars=None, ignore_robots=True, output=str(tmp_path / "f.csv"),
                                format="csv", json=True))


def test_cli_fetch_ats_partial_failure_keeps_good_source(tmp_path, fake_http, capsys):
    FakeClient.routes["https://api.ashbyhq.com/posting-api/job-board/linear"] = FakeResponse(
        json_data={"jobs": [{"id": "abc", "title": "E", "jobUrl": "https://jobs.ashbyhq.com/linear/x",
                             "isListed": True, "descriptionPlain": "plain"}]}
    )
    out = tmp_path / "f.csv"
    cli.cmd_fetch(Namespace(url=None, url_file=None, greenhouse_board=None, ashby_org="linear",
                            lever_org="ghost", max_jobs=None, delay=0, timeout=5, max_chars=None,
                            ignore_robots=True, output=str(out), format="csv", json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["items"] == 1
    assert any("ghost" in s.get("source", "") for s in payload["skipped"])


# --- YC directory ------------------------------------------------------------

YC_PAGE_1 = {"companies": [
    {"id": 1, "name": "KafkaOps", "slug": "kafkaops", "website": "https://kafkaops.example",
     "url": "https://www.ycombinator.com/companies/kafkaops",
     "oneLiner": "Managed Kafka for billing pipelines", "longDescription": "We run Kafka.",
     "teamSize": 8, "batch": "W24", "tags": ["B2B", "DevTools"], "industries": ["B2B"],
     "status": "Active"},
    {"id": 2, "name": "PhotoFun", "slug": "photofun", "website": "https://photofun.example",
     "url": "https://www.ycombinator.com/companies/photofun",
     "oneLiner": "Filters for pet photos", "longDescription": "Fun.",
     "teamSize": 3, "batch": "S23", "tags": ["Consumer"], "industries": ["Consumer"],
     "status": "Active"},
], "page": 1, "totalPages": 2}
YC_PAGE_2 = {"companies": [
    {"id": 3, "name": "DeadCo", "slug": "deadco", "website": "https://deadco.example",
     "url": "https://www.ycombinator.com/companies/deadco",
     "oneLiner": "Kafka things", "longDescription": "Inactive.",
     "teamSize": 1, "batch": "W20", "tags": ["B2B"], "industries": ["B2B"],
     "status": "Inactive"},
], "page": 2, "totalPages": 2}


class YCFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        page = (params or {}).get("page", 1)
        return FakeResponse(json_data={1: YC_PAGE_1, 2: YC_PAGE_2}[page])


def test_search_yc_matches_and_paginates(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", YCFakeClient)
    hits = discover.search_yc("kafka", max_results=5)
    assert [h.url for h in hits] == ["https://kafkaops.example"]
    assert hits[0].backend == "yc" and "W24" in hits[0].title


def test_fetch_yc_filters_and_grades_profiles(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", YCFakeClient)
    recs = discover.fetch_yc_companies(batch="W24")
    assert len(recs) == 1 and recs[0].item_id == "yc-kafkaops"
    assert recs[0].metadata["evidence"] == "profile"
    assert recs[0].metadata["team_size"] == 8
    assert "Managed Kafka" in recs[0].text and "Team size: 8" in recs[0].text
    tagged = discover.fetch_yc_companies(tags=["consumer"])
    assert [r.item_id for r in tagged] == ["yc-photofun"]
    assert discover.fetch_yc_companies(query="nonexistent-thing") == []


# --- Sitemaps + crawl ----------------------------------------------------------

def test_sitemap_locs_urlset_and_index():
    urlset = (b'<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
              b"<url><loc>https://a.example/1</loc></url>"
              b"<url><loc>https://a.example/2</loc></url></urlset>")
    sitemaps, pages = discover._sitemap_locs(urlset)
    assert (sitemaps, pages) == ([], ["https://a.example/1", "https://a.example/2"])
    index = (b"<sitemapindex><sitemap><loc>https://a.example/s1.xml</loc></sitemap>"
             b"<sitemap><loc>https://a.example/s2.xml</loc></sitemap></sitemapindex>")
    sitemaps, pages = discover._sitemap_locs(index)
    assert (sitemaps, pages) == (["https://a.example/s1.xml", "https://a.example/s2.xml"], [])
    import gzip

    sitemaps, pages = discover._sitemap_locs(gzip.compress(urlset))
    assert pages == ["https://a.example/1", "https://a.example/2"]
    with pytest.raises(DiscoverError, match="parse"):
        discover._sitemap_locs(b"not xml at all <<<")


def test_fetch_sitemap_recurses_and_caps(fake_http):
    FakeClient.routes["https://a.example/sitemap.xml"] = FakeResponse(content=(
        b"<sitemapindex><sitemap><loc>https://a.example/s1.xml</loc></sitemap></sitemapindex>"))
    FakeClient.routes["https://a.example/s1.xml"] = FakeResponse(content=(
        b"<urlset><url><loc>https://a.example/x</loc></url>"
        b"<url><loc>https://a.example/x</loc></url></urlset>"))
    urls = discover.fetch_sitemap_urls("https://a.example/sitemap.xml")
    assert urls == ["https://a.example/x"]
    assert discover.fetch_sitemap_urls("https://a.example/sitemap.xml", max_urls=1) == ["https://a.example/x"]


def test_discover_sitemap_url_prefers_robots_hint(fake_http):
    FakeClient.routes["https://a.example/robots.txt"] = FakeResponse(
        content=b"User-agent: *\nSitemap: https://a.example/custom-map.xml\n")
    FakeClient.routes["https://a.example/custom-map.xml"] = FakeResponse(
        content=b"<urlset><url><loc>https://a.example/x</loc></url></urlset>")
    assert discover.discover_sitemap_url("a.example") == "https://a.example/custom-map.xml"


def test_discover_sitemap_url_not_found(fake_http):
    with pytest.raises(DiscoverError, match="no sitemap found"):
        discover.discover_sitemap_url("https://a.example")


def test_extract_links_resolves_and_filters():
    html = ('<a href="/rel">r</a><a href="https://a.example/p#frag">p</a>'
            '<a href="https://other.example/">o</a><a href="mailto:x@y">m</a>')
    assert discover.extract_links(html, "https://a.example/base/") == [
        "https://a.example/rel", "https://a.example/p", "https://other.example/"]


def test_crawl_site_bfs_depth_and_origin(fake_http):
    home = ('<html><head><title>Home</title></head><body><p>home page</p>'
            '<a href="/docs">docs</a><a href="https://other.example/">ext</a></body></html>')
    docs = ('<html><head><title>Docs</title></head><body><p>docs page</p>'
            '<a href="/deep">deep</a></body></html>')
    FakeClient.routes["https://a.example/"] = FakeResponse(
        headers={"content-type": "text/html"}, content=home.encode(), url="https://a.example/")
    FakeClient.routes["https://a.example/docs"] = FakeResponse(
        headers={"content-type": "text/html"}, content=docs.encode(), url="https://a.example/docs")
    records, skipped = discover.crawl_site("https://a.example/", max_pages=10, max_depth=1, delay=0)
    assert sorted(r.source_uri for r in records) == ["https://a.example/", "https://a.example/docs"]
    assert skipped == []
    # depth 0 fetches only the start page
    records, _ = discover.crawl_site("https://a.example/", max_pages=10, max_depth=0, delay=0)
    assert [r.source_uri for r in records] == ["https://a.example/"]


# --- Evidence grading + JS seam --------------------------------------------------

def test_snippet_records_are_indicator_grade(monkeypatch):
    monkeypatch.setattr(discover, "search_hn", lambda q, max_results=10, client=None: [
        SearchHit(url="https://a.example/1", title="A", snippet="s", backend="hn"),
    ])
    items, _ = discover.run_discovery(["q"], backends=["hn"], fetch_full_text=False, delay=0)
    assert items[0].metadata["evidence"] == "indicator"


def test_fetched_records_are_fetched_grade(fake_http):
    FakeClient.routes["https://a.example/1"] = FakeResponse(
        headers={"content-type": "text/html"}, content=b"<p>hello</p>", url="https://a.example/1")
    rec = fetch_text("https://a.example/1", respect_robots=False)
    assert rec.metadata["evidence"] == "fetched"


def test_js_render_missing_playwright_errors_cleanly(fake_http):
    with pytest.raises(DiscoverError, match="account-fleet\\[js\\]"):
        fetch_text("https://a.example/1", respect_robots=False, render_js=True)


def test_require_playwright_hint(monkeypatch):
    import builtins

    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "playwright":
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)
    with pytest.raises(DiscoverError, match="account-fleet\\[js\\]"):
        discover.require_playwright()


def test_run_discovery_js_requires_playwright_upfront(monkeypatch):
    import builtins

    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "playwright":
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)
    with pytest.raises(DiscoverError, match="account-fleet\\[js\\]"):
        discover.run_discovery(["q"], backends=["hn"], render_js=True)


def test_js_render_success_path(monkeypatch, fake_http):
    class FakePage:
        def goto(self, url, timeout=None): pass
        def wait_for_load_state(self, state, timeout=None): pass
        def content(self): return "<html><head><title>JS app</title></head><body><p>rendered text</p></body></html>"

    class FakeBrowser:
        def new_page(self, user_agent=None): return FakePage()
        def close(self): pass

    class FakeChromium:
        def launch(self): return FakeBrowser()

    class FakePlaywright:
        chromium = FakeChromium()
        def __enter__(self): return self
        def __exit__(self, *a): return None

    pw = types.ModuleType("playwright")
    sync_api = types.ModuleType("playwright.sync_api")
    sync_api.sync_playwright = lambda: FakePlaywright()
    pw.sync_api = sync_api
    monkeypatch.setitem(sys.modules, "playwright", pw)
    monkeypatch.setitem(sys.modules, "playwright.sync_api", sync_api)
    rec = fetch_text("https://a.example/app", respect_robots=False, render_js=True)
    assert rec.text == "rendered text"
    assert rec.metadata == {"evidence": "fetched", "rendered": "js"}


def test_cli_discover_snippets_only_warns(tmp_path, monkeypatch, capsys):
    from free_fleet.discover import to_input_items as _to_items

    def fake_run(**kwargs):
        return _to_items([RawRecord(text="t", source_uri="https://a.example/")]), {
            "queries": ["q"], "hits": 1, "items": 1, "skipped": []}

    monkeypatch.setattr(cli, "run_discovery", fake_run)
    cli.cmd_discover(Namespace(query=["q"], backend=["hn"], searxng_url=None, max_results=5,
                               snippets_only=True, delay=0, timeout=5, max_chars=None, js=False,
                               ignore_robots=True, output=str(tmp_path / "o.csv"), format="csv", json=False))
    assert "indicators" in capsys.readouterr().out


def test_cli_fetch_yc_and_sitemap(tmp_path, fake_http, capsys, monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", YCFakeClient)
    out = tmp_path / "yc.csv"
    cli.cmd_fetch(Namespace(url=None, url_file=None, sitemap=None, site=None, greenhouse_board=None,
                            ashby_org=None, lever_org=None, yc=True, yc_query="kafka", yc_batch=None,
                            yc_tag=None, max_jobs=None, max_pages=20, max_depth=2, delay=0, timeout=5,
                            max_chars=None, js=False, ignore_robots=True, output=str(out),
                            format="csv", json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["items"] == 1 and out.is_file()


def test_cli_fetch_site_falls_back_to_crawl(tmp_path, fake_http, capsys):
    home = ('<html><head><title>Home</title></head><body><p>home page</p></body></html>')
    FakeClient.routes["https://a.example/robots.txt"] = FakeResponse(status=404, content=b"no")
    FakeClient.routes["https://a.example/sitemap.xml"] = FakeResponse(status=404, content=b"no")
    FakeClient.routes["https://a.example/sitemap_index.xml"] = FakeResponse(status=404, content=b"no")
    FakeClient.routes["https://a.example/"] = FakeResponse(
        headers={"content-type": "text/html"}, content=home.encode(), url="https://a.example/")
    FakeClient.routes["https://a.example"] = FakeClient.routes["https://a.example/"]
    out = tmp_path / "site.csv"
    cli.cmd_fetch(Namespace(url=None, url_file=None, sitemap=None, site="a.example", greenhouse_board=None,
                            ashby_org=None, lever_org=None, yc=False, yc_query=None, yc_batch=None,
                            yc_tag=None, max_jobs=None, max_pages=5, max_depth=0, delay=0, timeout=5,
                            max_chars=None, js=False, ignore_robots=False, output=str(out),
                            format="csv", json=True))
    assert json.loads(capsys.readouterr().out)["items"] == 1


# --- Community: backoff ----------------------------------------------------------

class ThrottleThenOkClient(FakeClient):
    calls = 0

    def get(self, url, params=None, timeout=None):
        ThrottleThenOkClient.calls += 1
        if ThrottleThenOkClient.calls < 3:
            return FakeResponse(status=429, headers={"retry-after": "0"}, content=b"slow")
        return FakeResponse(json_data={"data": []})


def test_backoff_retries_then_succeeds(monkeypatch, fake_http):
    sleeps = []
    monkeypatch.setattr("time.sleep", lambda s: sleeps.append(s))
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ThrottleThenOkClient)
    ThrottleThenOkClient.calls = 0
    resp = discover._get_with_backoff(ThrottleThenOkClient(), "https://x.example/api")
    assert resp.status_code == 200 and ThrottleThenOkClient.calls == 3 and len(sleeps) == 2


def test_backoff_gives_up_with_reason(monkeypatch, fake_http):
    monkeypatch.setattr("time.sleep", lambda s: None)

    class Always429(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(status=429, content=b"slow")

    monkeypatch.setattr("free_fleet.discover.httpx.Client", Always429)
    with pytest.raises(DiscoverError, match="after 4 attempts"):
        discover._get_with_backoff(Always429(), "https://x.example/api")


def test_backoff_treats_arctic_throttle_422_as_retryable(monkeypatch, fake_http):
    monkeypatch.setattr("time.sleep", lambda s: None)

    class ArcticThrottle(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(status=422, content=b'{"error":"Timeout. Maybe slow down a bit"}')

    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticThrottle)
    with pytest.raises(DiscoverError, match="throttled"):
        discover._get_with_backoff(ArcticThrottle(), "https://x.example/api")


def test_backoff_passes_through_real_422(fake_http):
    class Real422(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(status=422, content=b'{"error":"bad param"}')

    with pytest.raises(DiscoverError, match="HTTP 422"):
        discover._get_with_backoff(Real422(), "https://x.example/api")


# --- Community: Reddit -------------------------------------------------------------

ARCTIC_POSTS_PAGE = {"data": [
    {"id": "abc123", "title": "Kafka latency pain", "selftext": "We hit 50k QPS limits",
     "author": "u1", "subreddit": "dataengineering", "score": 42, "num_comments": 7,
     "permalink": "/r/dataengineering/comments/abc123/x/", "over_18": False},
    {"id": "dead1", "title": "Removed post", "selftext": "[removed]",
     "author": "u2", "subreddit": "dataengineering", "score": 1, "num_comments": 0,
     "permalink": "/r/dataengineering/comments/dead1/y/", "over_18": False},
    {"id": "nsfw1", "title": "Gone wild", "selftext": "nope",
     "author": "u3", "subreddit": "x", "score": 1, "num_comments": 0,
     "permalink": "/r/x/comments/nsfw1/z/", "over_18": True},
]}


class ArcticFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        url = str(url)
        if "posts/search" in url:
            return FakeResponse(json_data=ARCTIC_POSTS_PAGE)
        if "comments/search" in url:
            return FakeResponse(json_data={"data": [
                {"id": "c1", "body": "Second: use partitioning", "author": "helper",
                 "subreddit": "dataengineering", "score": 30, "link_id": "t3_abc123"},
                {"id": "c2", "body": "[deleted]", "author": "[deleted]",
                 "subreddit": "dataengineering", "score": 99, "link_id": "t3_abc123"},
                {"id": "c3", "body": "First: check brokers", "author": "pro",
                 "subreddit": "dataengineering", "score": 5, "link_id": "t3_abc123"},
            ]})
        return FakeResponse(status=404, content=b"no")


def test_search_reddit_hits_and_filters(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticFakeClient)
    hits = discover.search_reddit("kafka", subreddits=["dataengineering"], max_results=10)
    assert len(hits) == 2  # full post + title-only removed post; nsfw excluded
    assert hits[0].backend == "reddit" and "50k QPS" in hits[0].snippet


def test_fetch_reddit_posts_complete_records(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticFakeClient)
    recs = discover.fetch_reddit_posts("kafka", subreddits=["dataengineering"])
    assert [r.item_id for r in recs] == ["reddit-abc123", "reddit-dead1"]
    assert recs[0].metadata["evidence"] == "profile" and recs[0].metadata["score"] == 42
    assert "50k QPS" in recs[0].text


RSS_SAMPLE = """<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<entry><title>Showcase: my tool</title>
<content type="html">&lt;p&gt;Hello &amp; welcome&lt;/p&gt;</content>
<link href="https://www.reddit.com/r/test/comments/xyz/post/"/>
<author><name>/u/someone</name></author>
<id>t3_xyz</id></entry>
</feed>"""


def test_fetch_reddit_rss_parses_entries(fake_http):
    FakeClient.routes["https://www.reddit.com/r/test/new/.rss"] = FakeResponse(content=RSS_SAMPLE.encode())
    recs = discover.fetch_reddit_rss("test")
    assert len(recs) == 1
    assert recs[0].item_id == "reddit-xyz" and "Hello & welcome" in recs[0].text
    assert recs[0].metadata["source"] == "reddit-rss"


def test_fetch_reddit_rss_rejects_sort():
    with pytest.raises(DiscoverError, match="unknown subreddit sort"):
        discover.fetch_reddit_rss("test", sort="bogus")


def test_fetch_reddit_thread_sorts_and_skips_deleted(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticFakeClient)
    rec = discover.fetch_reddit_thread("https://www.reddit.com/r/dataengineering/comments/abc123/x/")
    assert rec.item_id == "reddit-thread-abc123"
    assert "[deleted]" not in rec.text
    assert rec.text.index("partitioning") < rec.text.index("brokers")  # score order
    with pytest.raises(DiscoverError, match="not a Reddit post"):
        discover.fetch_reddit_thread("not a url at all!!!")


# --- Community: HN threads -----------------------------------------------------------

HN_ITEMS = {
    "100": {"id": 100, "type": "story", "title": "Kafka at scale", "text": "Ask post",
            "url": "https://kafka.example", "score": 200, "descendants": 3, "kids": [101, 102]},
    "101": {"id": 101, "type": "comment", "by": "alice", "text": "First &amp; best <p>point</p>", "kids": [103]},
    "102": {"id": 102, "type": "comment", "by": "ghost", "text": "gone", "deleted": True, "kids": []},
    "103": {"id": 103, "type": "comment", "by": "bob", "text": "Nested reply", "dead": True, "kids": []},
}


class HNFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        item_id = str(url).rstrip(".json").rsplit("/", 1)[-1]
        item = HN_ITEMS.get(item_id)
        if item is None:
            return FakeResponse(status=404, content=b"null")
        return FakeResponse(json_data=item)


def test_fetch_hn_thread_full_walk(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", HNFakeClient)
    rec = discover.fetch_hn_thread("https://news.ycombinator.com/item?id=100")
    assert rec.item_id == "hn-100" and rec.metadata["evidence"] == "profile"
    assert "Kafka at scale" in rec.text and "https://kafka.example" in rec.text
    assert "[alice]: First & best" in rec.text and "point" in rec.text
    assert "gone" not in rec.text and "Nested reply" not in rec.text  # deleted + dead skipped
    assert discover.fetch_hn_thread("100").item_id == "hn-100"
    with pytest.raises(DiscoverError, match="not an HN item"):
        discover.fetch_hn_thread("abc")


def test_fetch_hn_thread_missing_item(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", HNFakeClient)
    with pytest.raises(DiscoverError, match="not found"):
        discover.fetch_hn_thread("999")


def test_smart_url_routing(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", HNFakeClient)
    rec = discover.fetch_smart_url("https://news.ycombinator.com/item?id=100")
    assert rec.item_id == "hn-100"
    assert discover._hn_item_id_from_url("https://example.com/") is None
    assert discover._reddit_post_id_from_url("https://www.reddit.com/r/x/comments/abc123/t/") == "abc123"
    assert discover._reddit_post_id_from_url("https://example.com/") is None


def test_run_discovery_uses_smart_urls(monkeypatch):
    monkeypatch.setattr(discover, "search_hn", lambda q, max_results=10, client=None: [
        SearchHit(url="https://news.ycombinator.com/item?id=100", title="t", snippet="s", backend="hn"),
    ])
    monkeypatch.setattr("free_fleet.discover.httpx.Client", HNFakeClient)
    items, report = discover.run_discovery(["q"], backends=["hn"], delay=0)
    assert items[0].item_id == "hn-100" and report["hits"] == 1


# --- Community: CLI ---------------------------------------------------------------------

def test_cli_fetch_subreddit_and_hn(tmp_path, fake_http, capsys, monkeypatch):
    class BothClient(HNFakeClient):
        def get(self, url, params=None, timeout=None):
            if "reddit.com" in str(url):
                return FakeResponse(content=RSS_SAMPLE.encode())
            return super().get(url, params=params, timeout=timeout)

    monkeypatch.setattr("free_fleet.discover.httpx.Client", BothClient)
    out = tmp_path / "c.csv"
    cli.cmd_fetch(Namespace(url=None, url_file=None, sitemap=None, site=None, subreddit=["test"],
                            subreddit_sort="new", hn=["100"], greenhouse_board=None, ashby_org=None,
                            lever_org=None, yc=False, yc_query=None, yc_batch=None, yc_tag=None,
                            max_jobs=None, max_pages=20, max_depth=2, max_comments=50, delay=0,
                            timeout=5, max_chars=None, js=False, ignore_robots=True,
                            output=str(out), format="csv", json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["items"] == 2 and out.is_file()


def test_cli_discover_reddit_backend(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(discover, "search_reddit", lambda q, subreddits=(), max_results=10, client=None: [
        SearchHit(url="https://www.reddit.com/r/x/comments/a/b/", title="t", snippet="s", backend="reddit"),
    ])
    monkeypatch.setattr(discover, "fetch_smart_url", lambda url, **kw: RawRecord(
        text="full post", source_uri=url, title="t"))
    out = tmp_path / "r.csv"
    cli.cmd_discover(Namespace(query=["kafka pain"], backend=["reddit"], subreddit=["dataengineering"],
                               searxng_url=None, max_results=5, snippets_only=False, delay=0,
                               timeout=5, max_chars=None, js=False, ignore_robots=True,
                               output=str(out), format="csv", json=True))
    assert json.loads(capsys.readouterr().out)["items"] == 1


# --- Q&A: Stack Exchange -----------------------------------------------------------

SE_QUESTION = {"question_id": 1, "title": "Kafka pain?", "link": "https://stackoverflow.com/q/1",
               "score": 50, "answer_count": 3, "tags": ["apache-kafka"],
               "body": "<p>We hit limits at 50k QPS</p>"}
SE_SEARCH_PAGE = {"items": [dict(SE_QUESTION)], "quota_remaining": 291, "quota_max": 300}


class SEFakeClient(FakeClient):
    last_params: dict = {}

    def get(self, url, params=None, timeout=None):
        SEFakeClient.last_params = dict(params or {})
        url = str(url)
        if url.endswith("/search/advanced"):
            return FakeResponse(json_data=SE_SEARCH_PAGE)
        if "/answers" in url:
            return FakeResponse(json_data={"items": [{"body": "<p>Use partitions</p>", "score": 44}],
                                           "quota_remaining": 290, "quota_max": 300})
        return FakeResponse(status=404, content=b"no")


def test_se_search_hits_and_tagged_param(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", SEFakeClient)
    hits = discover.search_stackexchange("kafka", tagged=["apache-kafka"])
    assert SEFakeClient.last_params.get("tagged") == "apache-kafka"
    assert hits[0].backend == "stackexchange" and "Kafka pain" in hits[0].title


def test_se_fetch_full_with_answer_and_quota(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", SEFakeClient)
    recs = discover.fetch_stackexchange_questions("kafka", include_answers=True)
    assert len(recs) == 1 and recs[0].item_id == "se-stackoverflow-1"
    assert "50k QPS" in recs[0].text and "Use partitions" in recs[0].text
    assert recs[0].metadata["evidence"] == "profile"
    assert recs[0].metadata["quota_remaining"] == 290  # answers call ran last
    plain = discover.fetch_stackexchange_questions("kafka")
    assert "Use partitions" not in plain[0].text


def test_se_api_error_surfaces(monkeypatch):
    class SEError(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(json_data={"error_id": 502, "error_message": "throttle"})

    monkeypatch.setattr("free_fleet.discover.httpx.Client", SEError)
    with pytest.raises(DiscoverError, match="throttle"):
        discover.search_stackexchange("kafka")


def test_se_backoff_field_honored(monkeypatch):
    sleeps = []
    monkeypatch.setattr("time.sleep", lambda s: sleeps.append(s))

    class SEBackoff(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(json_data={"items": [], "backoff": 5, "quota_remaining": 1})

    monkeypatch.setattr("free_fleet.discover.httpx.Client", SEBackoff)
    assert discover.search_stackexchange("kafka") == [] and sleeps == [5]


# --- Q&A: Discourse ---------------------------------------------------------------------

DISCO_SEARCH = {"posts": [
    {"id": 1, "topic_id": 7, "username": "u1", "blurb": "first <b>mention</b> here"},
    {"id": 2, "topic_id": 7, "username": "u2", "blurb": "second mention here"},
    {"id": 3, "topic_id": 8, "username": "u3", "blurb": "other topic"},
], "topics": [{"id": 7, "fancy_title": "Kafka &amp; pain"}, {"id": 8, "fancy_title": "Other"}]}
DISCO_TOPIC = {"title": "Kafka & pain",
               "post_stream": {"posts": [
                   {"post_type": 1, "deleted_at": None, "cooked": "<p>OP body</p>", "username": "op"},
                   {"post_type": 3, "deleted_at": None, "cooked": "<p>small action</p>", "username": "sys"},
                   {"post_type": 1, "deleted_at": "2024-01-01", "cooked": "<p>gone</p>", "username": "x"},
                   {"post_type": 1, "deleted_at": None, "cooked": "<p>Reply body</p>", "username": "re"},
               ]}}


class DiscoFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        url = str(url)
        if url.endswith("/search.json"):
            return FakeResponse(json_data=DISCO_SEARCH)
        if url.endswith("/latest.json"):
            return FakeResponse(json_data={"topic_list": {"topics": [{"id": 9}, {"id": 7}]}})
        if "/t/7.json" in url:
            return FakeResponse(json_data=DISCO_TOPIC)
        if "/t/9.json" in url:
            return FakeResponse(json_data={"title": "Fresh", "post_stream": {"posts": [
                {"post_type": 1, "deleted_at": None, "cooked": "<p>fresh post</p>", "username": "n"}]}})
        return FakeResponse(status=404, content=b"no")


def test_disco_search_dedupes_topics_and_unescapes(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", DiscoFakeClient)
    hits = discover.search_discourse("kafka", base_url="https://forum.example")
    assert [h.url for h in hits] == ["https://forum.example/t/7", "https://forum.example/t/8"]
    assert hits[0].title == "Kafka & pain" and hits[0].backend == "discourse"


def test_disco_topic_skips_non_posts(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", DiscoFakeClient)
    rec = discover.fetch_discourse_topic("forum.example", 7)
    assert rec.item_id == "discourse-forum.example-7"
    assert "OP body" in rec.text and "Reply body" in rec.text
    assert "small action" not in rec.text and "gone" not in rec.text
    assert rec.metadata["evidence"] == "profile"


def test_disco_search_fetch_latest_fallback(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", DiscoFakeClient)
    records, skipped = discover.fetch_discourse_search("https://forum.example", None, max_topics=2)
    assert [r.item_id for r in records] == ["discourse-forum.example-9", "discourse-forum.example-7"]
    assert skipped == []


def test_disco_missing_topic_errors(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", DiscoFakeClient)
    with pytest.raises(DiscoverError, match="not found"):
        discover.fetch_discourse_topic("https://forum.example", 404)


# --- Q&A: Lobsters / Lemmy / Dev.to -------------------------------------------------------

LOBSTERS_PAGE = [
    {"short_id": "a1", "title": "Why Kafka wins", "url": "https://x.example/k",
     "description": "", "description_plain": "On logs and pain",
     "comments_url": "https://lobste.rs/s/a1/x", "short_id_url": "https://lobste.rs/s/a1",
     "score": 10, "comment_count": 4, "tags": ["kafka", "ops"]},
    {"short_id": "b2", "title": "Cute cats", "url": "https://x.example/c",
     "description": "", "description_plain": "meow",
     "comments_url": "https://lobste.rs/s/b2/y", "short_id_url": "https://lobste.rs/s/b2",
     "score": 3, "comment_count": 1, "tags": ["offtopic"]},
]


class LobstersFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        if "lobste.rs" in str(url):
            return FakeResponse(json_data=LOBSTERS_PAGE)
        return FakeResponse(status=404, content=b"no")


def test_lobsters_listing_and_filter(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", LobstersFakeClient)
    recs = discover.fetch_lobsters()
    assert [r.item_id for r in recs] == ["lobsters-a1", "lobsters-b2"]
    assert recs[0].source_uri == "https://lobste.rs/s/a1/x"
    assert "On logs and pain" in recs[0].text
    hits = discover.search_lobsters("kafka ops")
    assert [h.url for h in hits] == ["https://lobste.rs/s/a1/x"]


def test_lobsters_unknown_tag(monkeypatch):
    class Lobsters404(FakeClient):
        def get(self, url, params=None, timeout=None):
            return FakeResponse(status=404, content=b"no")

    monkeypatch.setattr("free_fleet.discover.httpx.Client", Lobsters404)
    with pytest.raises(DiscoverError, match="unknown Lobsters tag"):
        discover.fetch_lobsters(tag="nope")


LEMMY_PAGE = {"posts": [
    {"post": {"id": 11, "name": "Kafka help", "body": "Stuck at scale",
              "ap_id": "https://lem.example/p/11", "published": "2026-01-01",
              "removed": False, "deleted": False, "nsfw": False}},
    {"post": {"id": 12, "name": "Gone", "body": "x",
              "ap_id": "https://lem.example/p/12", "published": "",
              "removed": True, "deleted": False, "nsfw": False}},
], "comments": [
    {"comment": {"id": 21, "content": "Try more partitions", "ap_id": "https://lem.example/c/21",
                 "published": "", "removed": False, "deleted": False}},
]}


class LemmyFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        if "/api/v3/search" in str(url):
            return FakeResponse(json_data=LEMMY_PAGE)
        return FakeResponse(status=404, content=b"no")


def test_lemmy_posts_comments_and_filters(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", LemmyFakeClient)
    recs = discover.fetch_lemmy("kafka")
    assert [r.item_id for r in recs] == ["lemmy-11", "lemmy-c-21"]
    assert "Stuck at scale" in recs[0].text and recs[0].metadata["evidence"] == "profile"
    hits = discover.search_lemmy("kafka")
    assert len(hits) == 2 and hits[0].backend == "lemmy"


DEVTO_LIST = [{"id": 5, "title": "Kafka guide", "description": "Learn streams",
               "url": "https://dev.to/u/kafka-guide", "tag_list": ["kafka"],
               "public_reactions_count": 9}]


class DevtoFakeClient(FakeClient):
    def get(self, url, params=None, timeout=None):
        url = str(url)
        if url.endswith("/api/articles"):
            return FakeResponse(json_data=DEVTO_LIST)
        if url.endswith("/api/articles/5"):
            return FakeResponse(json_data={"body_markdown": "# Guide\n\nFull body here"})
        return FakeResponse(status=404, content=b"no")


def test_devto_full_and_indicator_grades(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", DevtoFakeClient)
    recs = discover.fetch_devto_tag("kafka")
    assert recs[0].item_id == "devto-5" and "Full body here" in recs[0].text
    assert recs[0].metadata["evidence"] == "profile"
    thin = discover.fetch_devto_tag("kafka", full_body=False)
    assert thin[0].metadata["evidence"] == "indicator" and "Learn streams" in thin[0].text
    hits = discover.search_devto("kafka streams")
    assert [h.url for h in hits] == ["https://dev.to/u/kafka-guide"]


# --- Q&A: dispatch + CLI ----------------------------------------------------------------------

def test_run_discovery_validates_discourse_url():
    with pytest.raises(DiscoverError, match="discourse-url"):
        discover.run_discovery(["q"], backends=["discourse"])


def test_run_discovery_new_backends(monkeypatch):
    monkeypatch.setattr(discover, "search_stackexchange", lambda q, **kw: [
        SearchHit(url="https://stackoverflow.com/q/1", title="t", snippet="s", backend="stackexchange")])
    monkeypatch.setattr(discover, "fetch_smart_url", lambda url, **kw: RawRecord(
        text="full", source_uri=url, title="t"))
    items, report = discover.run_discovery(["q"], backends=["stackexchange"], delay=0)
    assert items[0].text == "full" and report["hits"] == 1


def _qa_namespace(tmp_path, **over):
    base = dict(url=None, url_file=None, sitemap=None, site=None, subreddit=None, reddit_query=None,
                hn=None, stackexchange_query=None, se_tag=None, se_site="stackoverflow", se_answers=False,
                discourse=None, discourse_query=None, lobsters_tag=None, lemmy_query=None,
                lemmy_instance="https://programming.dev", devto_tag=None,
                greenhouse_board=None, ashby_org=None, lever_org=None, yc=False, yc_query=None,
                yc_batch=None, yc_tag=None, max_jobs=None, max_pages=20, max_depth=2, max_comments=50,
                delay=0, timeout=5, max_chars=None, js=False, ignore_robots=True,
                output=str(tmp_path / "q.csv"), format="csv", json=True)
    base.update(over)
    return Namespace(**base)


def test_run_discovery_reports_zero_hit_queries(monkeypatch):
    monkeypatch.setattr(discover, "search_hn", lambda q, max_results=10, client=None: [])
    items, report = discover.run_discovery(["nothing matches this"], backends=["hn"], delay=0)
    assert items == [] and report["skipped"] == [{"query": "nothing matches this",
                                                  "reason": "0 hits from backends"}]


def test_cli_fetch_stackexchange(tmp_path, capsys, monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", SEFakeClient)
    cli.cmd_fetch(_qa_namespace(tmp_path, stackexchange_query="kafka", se_tag=["apache-kafka"]))
    payload = json.loads(capsys.readouterr().out)
    assert payload["items"] == 1
    assert SEFakeClient.last_params.get("tagged") == "apache-kafka"


def test_cli_fetch_discourse_lobsters_lemmy_devto(tmp_path, capsys, monkeypatch):
    class QAClient(DiscoFakeClient):
        def get(self, url, params=None, timeout=None):
            url = str(url)
            if "lobste.rs" in url:
                return FakeResponse(json_data=LOBSTERS_PAGE)
            if "/api/v3/search" in url:
                return FakeResponse(json_data=LEMMY_PAGE)
            if "dev.to" in url:
                if url.endswith("/api/articles"):
                    return FakeResponse(json_data=DEVTO_LIST)
                return FakeResponse(json_data={"body_markdown": "body"})
            return super().get(url, params=params, timeout=timeout)

    monkeypatch.setattr("free_fleet.discover.httpx.Client", QAClient)
    cli.cmd_fetch(_qa_namespace(tmp_path, discourse="https://forum.example", discourse_query="kafka",
                                lobsters_tag="", lemmy_query="kafka", devto_tag="kafka"))
    payload = json.loads(capsys.readouterr().out)
    # discourse topic 7 ok, topic 8 missing (skipped, not fatal); 2 lobsters + 2 lemmy + 1 devto
    assert payload["items"] == 1 + 2 + 2 + 1, payload
    assert len(payload["skipped"]) == 1 and "t/8" in payload["skipped"][0]["source"]


# --- Bug-fix regressions (adversarial review round) ------------------------------

def test_backend_registry_matches_dispatch():
    """Every registered backend must be explicitly wired; no silent fallthrough."""
    import inspect
    src = inspect.getsource(discover._run_backend)
    for name in discover.BACKENDS:
        assert f'"{name}"' in src, f"backend {name!r} not dispatched in _run_backend"
    assert "search backend not wired" in src


def test_unwired_backend_raises_not_hn(monkeypatch):
    monkeypatch.setitem(discover.BACKENDS, "phantom", lambda *a, **k: [])
    with pytest.raises(DiscoverError, match="not wired"):
        discover._run_backend("phantom", "q", 5, None, FakeClient())


def test_article_header_text_survives_fallback():
    html = ('<html><head><title>T</title></head><body>'
            '<header class="site-header"><p>Site chrome</p></header>'
            '<article><header><h1>The Real Title</h1><p>By A. Author</p></header>'
            '<p>Body about Kafka.</p></article></body></html>')
    text = discover.extract_text(html)
    assert "The Real Title" in text and "By A. Author" in text
    assert "Site chrome" not in text


def test_gzip_sitemap_detected(fake_http):
    import gzip as _gzip
    gz = _gzip.compress(b"<urlset><url><loc>https://a.example/x</loc></url></urlset>")
    FakeClient.routes["https://a.example/robots.txt"] = FakeResponse(
        content=b"User-agent: *\nSitemap: https://a.example/sitemap.xml.gz\n")
    FakeClient.routes["https://a.example/sitemap.xml"] = FakeResponse(status=404, content=b"no")
    FakeClient.routes["https://a.example/sitemap_index.xml"] = FakeResponse(status=404, content=b"no")
    FakeClient.routes["https://a.example/sitemap.xml.gz"] = FakeResponse(content=gz)
    assert discover.discover_sitemap_url("a.example") == "https://a.example/sitemap.xml.gz"


def test_sitemap_partial_child_failure_keeps_good_urls(fake_http, monkeypatch):
    monkeypatch.setattr("time.sleep", lambda s: None)
    FakeClient.routes["https://a.example/sitemap.xml"] = FakeResponse(content=(
        b"<sitemapindex><sitemap><loc>https://a.example/missing.xml</loc></sitemap>"
        b"<sitemap><loc>https://a.example/good.xml</loc></sitemap></sitemapindex>"))
    FakeClient.routes["https://a.example/good.xml"] = FakeResponse(content=(
        b"<urlset><url><loc>https://a.example/page</loc></url></urlset>"))
    urls = discover.fetch_sitemap_urls("https://a.example/sitemap.xml")
    assert urls == ["https://a.example/page"]


def test_reddit_id_extraction_edge_shapes():
    cases = {
        "https://www.reddit.com/r/x/comments/abc123": "abc123",
        "https://www.reddit.com/r/x/comments/abc123/": "abc123",
        "https://www.reddit.com/r/x/comments/abc123/some_slug/?utm=1": "abc123",
        "https://www.reddit.com/comments/abc123": "abc123",
        "https://redd.it/abc123": "abc123",
        "https://www.reddit.com/r/x/": None,
        "https://example.com/comments/abc123": None,
    }
    for url, expected in cases.items():
        assert discover._reddit_post_id_from_url(url) == expected, url


def test_fetch_reddit_thread_accepts_bare_and_short_urls(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticFakeClient)
    for ref in ("abc123", "https://www.reddit.com/r/x/comments/abc123",
                "https://www.reddit.com/r/x/comments/abc123/slug/", "https://redd.it/abc123"):
        assert discover.fetch_reddit_thread(ref).item_id == "reddit-thread-abc123"


def test_smart_url_routes_reddit_no_slash(monkeypatch):
    monkeypatch.setattr("free_fleet.discover.httpx.Client", ArcticFakeClient)
    rec = discover.fetch_smart_url("https://www.reddit.com/r/x/comments/abc123")
    assert rec.item_id == "reddit-thread-abc123"


def test_lobsters_search_skips_empty_source_uri(monkeypatch):
    recs = [
        RawRecord(text="kafka story", source_uri="", title="no url"),
        RawRecord(text="kafka story two", source_uri="https://lobste.rs/s/ok", title="ok"),
    ]
    monkeypatch.setattr(discover, "fetch_lobsters", lambda **kw: recs)
    hits = discover.search_lobsters("kafka")
    assert [h.url for h in hits] == ["https://lobste.rs/s/ok"]


def test_lobsters_uses_backoff(monkeypatch):
    slept = []
    monkeypatch.setattr("time.sleep", lambda s: slept.append(s))

    class Twice429(FakeClient):
        calls = 0

        def get(self, url, params=None, timeout=None):
            Twice429.calls += 1
            if Twice429.calls < 3:
                return FakeResponse(status=429, headers={"retry-after": "0"}, content=b"slow")
            return FakeResponse(json_data=LOBSTERS_PAGE)

    monkeypatch.setattr("free_fleet.discover.httpx.Client", Twice429)
    Twice429.calls = 0
    recs = discover.fetch_lobsters()
    assert len(recs) == 2 and Twice429.calls == 3 and slept == [0.0, 0.0]  # retry-after: 0 honored


def test_devto_uses_backoff_for_details(monkeypatch):
    slept = []
    monkeypatch.setattr("time.sleep", lambda s: slept.append(s))

    class FlakyDetail(FakeClient):
        detail_calls = 0

        def get(self, url, params=None, timeout=None):
            url = str(url)
            if url.endswith("/api/articles"):
                return FakeResponse(json_data=DEVTO_LIST)
            FlakyDetail.detail_calls += 1
            if FlakyDetail.detail_calls < 2:
                return FakeResponse(status=429, headers={"retry-after": "0"}, content=b"slow")
            return FakeResponse(json_data={"body_markdown": "Full body"})

    monkeypatch.setattr("free_fleet.discover.httpx.Client", FlakyDetail)
    FlakyDetail.detail_calls = 0
    recs = discover.fetch_devto_tag("kafka")
    assert "Full body" in recs[0].text and slept == [0.0]  # retry-after: 0 honored
