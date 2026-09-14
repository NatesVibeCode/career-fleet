"""Ports of harness-fleet parser/bug fixes into career's discover fork.

Each test pins one ported behavior: stable ATS identity, evidence grades,
lineage, HTTP error context, and the shared parser gates (UTM, boilerplate
rank, script-leak, JSON-LD, sitemap lastmod, YC coverage, crawl pacing).
"""
import pytest

from harness_fleet import discover
from harness_fleet.discover import DiscoverError


class FakeResponse:
    def __init__(self, *, status=200, headers=None, content=b"", json_data=None, url="https://example.com/"):
        self.status_code = status
        self.headers = headers or {}
        self.content = content
        self._json = json_data
        self.url = url

    @property
    def is_redirect(self):
        return 300 <= self.status_code < 400

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

    def get(self, url, params=None, timeout=None, follow_redirects=None):
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
    monkeypatch.setattr("harness_fleet.discover.httpx.Client", FakeClient)
    return FakeClient


def test_ats_records_carry_profile_evidence(fake_http):
    FakeClient.routes["https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true"] = FakeResponse(
        json_data={"jobs": [{
            "id": 1, "title": "Engineer",
            "absolute_url": "https://boards.greenhouse.io/acme/jobs/1",
            "content": "<p>Kafka work here</p>",
        }]})
    recs = discover.fetch_greenhouse_board("acme")
    assert len(recs) == 1
    assert recs[0].metadata["evidence"] == "profile"


def test_ats_identity_is_position_independent(fake_http):
    payload = {"jobs": [
        {"title": "No identifiers at all", "absolute_url": "",
         "content": "<p>stable identifying text about Kafka</p>"},
    ]}
    FakeClient.routes["https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true"] = FakeResponse(
        json_data=payload)
    first = discover.fetch_greenhouse_board("acme")[0].item_id
    second = discover.fetch_greenhouse_board("acme")[0].item_id
    assert first == second
    assert not first.startswith("row-")


def test_canonical_url_strips_tracking_params():
    assert discover.canonical_url("https://a.example/p?utm_source=x&gclid=y") == "https://a.example/p"
    assert discover.canonical_url("https://a.example/p?page=2&utm_medium=z") == "https://a.example/p?page=2"


def test_article_outranks_chrome_hints():
    from harness_fleet.discover import _FallbackExtractor

    html = ('<html><body><div class="promo-banner">outside noise</div>'
            '<article><div class="promo-copy">inside copy</div></article></body></html>')
    parser = _FallbackExtractor()
    parser.feed(html)
    text = parser.get_text()
    assert "inside copy" in text
    assert "outside noise" not in text


def test_hidden_blocks_never_leak_into_text():
    from harness_fleet.discover import _fallback_strip

    html = ('<html><head><script type="application/ld+json">{"@type": "X"}</script>'
            '<style>.a{color:red}</style></head><body></body></html>')
    assert _fallback_strip(html).strip() == ""


def test_json_ld_backfills_thin_pages():
    from harness_fleet.discover import _record_from_response

    body = ("Kafka clusters at serious scale require careful partition planning "
            "and exactly-once semantics across regions. " * 6)
    html = (f'<html><head><title>Thin</title><script type="application/ld+json">'
            f'{{"@type": "TechArticle", "articleBody": "{body}"}}</script></head>'
            f"<body><div id=\"app\"></div></body></html>")
    record = _record_from_response("https://a.example/p", "text/html", html.encode(), "https://a.example/p")
    assert "partition planning" in record.text
    assert record.metadata["format"] == "json-ld"


def test_sitemap_entries_carry_lastmod():
    urlset = (b'<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
              b"<url><loc>https://a.example/1</loc><lastmod>2026-09-01</lastmod></url>"
              b"<url><loc>https://a.example/2</loc></url></urlset>")
    _, entries = discover._sitemap_entries(urlset)
    assert entries == [("https://a.example/1", "2026-09-01"), ("https://a.example/2", None)]


def test_snippet_records_carry_discovery_lineage(monkeypatch):
    from harness_fleet import discover as d

    hit = d.SearchHit(url="https://a.example/p", title="T", snippet="some snippet text here", backend="ddgs")
    monkeypatch.setattr(d, "_run_backend", lambda *a, **k: [hit])
    items, _ = d.run_discovery(["kafka"], backends=["ddgs"], fetch_full_text=False)
    assert len(items) == 1
    assert items[0].metadata["evidence"] == "indicator"
    assert items[0].metadata["discovery_query"] == "kafka"
    assert items[0].metadata["discovered_from"] == "https://a.example/p"


def test_searxng_http_error_carries_status(fake_http):
    FakeClient.routes["http://localhost:8888/search"] = FakeResponse(
        status=503, content=b"<html>maintenance</html>")
    with pytest.raises(DiscoverError, match="SearXNG returned HTTP 503"):
        discover.search_searxng("q", base_url="http://localhost:8888")


def test_crawl_paces_per_origin(fake_http, monkeypatch):
    home = ('<html><head><title>Home</title></head><body><p>home page</p>'
            '<a href="https://other.example/">ext</a></body></html>')
    ext = '<html><head><title>Ext</title></head><body><p>ext page</p></body></html>'
    FakeClient.routes["https://a.example/"] = FakeResponse(
        headers={"content-type": "text/html"}, content=home.encode(), url="https://a.example/")
    FakeClient.routes["https://other.example/"] = FakeResponse(
        headers={"content-type": "text/html"}, content=ext.encode(), url="https://other.example/")
    sleeps = []
    monkeypatch.setattr(discover.time, "sleep", lambda s: sleeps.append(s))
    records, _ = discover.crawl_site("https://a.example/", max_pages=10, max_depth=1,
                                     same_origin=False, delay=30)
    assert len(records) == 2
    assert sleeps == [] or all(s < 30 for s in sleeps)


def test_ats_field_text_reads_title_variants():
    assert discover._ats_field_text({"title": "Remote"}) == "Remote"
    assert discover._ats_field_text({"name": "N", "title": "T"}) == "N"


def test_fetch_yc_records_carry_page_coverage(monkeypatch):
    page1 = {"companies": [
        {"id": 1, "name": "KafkaOps", "slug": "kafkaops", "website": "https://kafkaops.example",
         "url": "https://www.ycombinator.com/companies/kafkaops", "oneLiner": "Managed Kafka",
         "longDescription": "Kafka.", "teamSize": 8, "batch": "W24", "tags": ["B2B"],
         "industries": ["B2B"], "status": "Active"},
    ], "page": 1, "totalPages": 2}
    page2 = {"companies": [
        {"id": 2, "name": "OldCo", "slug": "oldco", "website": "https://oldco.example",
         "url": "https://www.ycombinator.com/companies/oldco", "oneLiner": "Old",
         "longDescription": "Old.", "teamSize": 2, "batch": "W20", "tags": ["B2B"],
         "industries": ["B2B"], "status": "Active"},
    ], "page": 2, "totalPages": 2}

    class YCClient(FakeClient):
        def get(self, url, params=None, timeout=None, follow_redirects=None):
            return FakeResponse(json_data={1: page1, 2: page2}[(params or {}).get("page", 1)])

    monkeypatch.setattr("harness_fleet.discover.httpx.Client", YCClient)
    recs = discover.fetch_yc_companies()
    assert len(recs) == 2
    for rec in recs:
        assert rec.metadata["yc_total_pages"] == 2
        assert rec.metadata["yc_pages_scanned"] >= 1
    capped = discover.fetch_yc_companies(max_companies=1, delay=0)
    assert len(capped) == 1
    assert capped[0].metadata["yc_pages_scanned"] == 1
