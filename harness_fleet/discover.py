"""Mechanical web discovery: broad search, polite fetch, text parse, ATS intake.

This module is the mechanical counterpart to the agent-guided discovery
playbook: instead of hand-running ``site:`` queries, ``discover`` searches the
broad web, fetches hits, and parses them into :class:`InputItem` records whose
``text`` is stored verbatim so downstream quote grounding keeps working.

Search backends: ``ddgs`` metasearch, self-hosted SearXNG, HN Algolia, and the
YC company directory (all keyless). Intake: arbitrary URLs, sitemaps,
same-domain crawls, Greenhouse/Ashby/Lever JSON APIs, YC profiles.

Hard dependencies: stdlib + ``httpx`` (already required). Broad web search
(``ddgs``, MIT) and high-fidelity article extraction (``trafilatura``,
Apache-2.0, then ``readability-lxml``, Apache-2.0) light up when the optional
``discover`` extra is installed::

    pip install career-fleet[discover]

JS-heavy pages render via the optional ``js`` extra (Playwright, experimental)::

    pip install career-fleet[js] && playwright install chromium

Every entry point degrades to a clear install hint when an optional backend is
missing. Keyless structured sources (Greenhouse/Ashby JSON APIs, HN Algolia,
YC directory) work with the base install.

Evidence grades in ``metadata["evidence"]``: ``fetched`` (full page text,
grounding-grade), ``profile`` (directory/ATS structured text, indicator),
``indicator`` (search snippet, triage only — never backs tier-1 claims).
"""
from __future__ import annotations

import csv
import json
import re
import time
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from html.parser import HTMLParser
from pathlib import Path
from typing import Any
from urllib.parse import urlparse, urlunparse

import httpx

from .models import InputItem

USER_AGENT = "fleet-discover (+https://github.com/NatesVibeCode/career-fleet)"
DISCOVER_EXTRA = "pip install career-fleet[discover] (or account-fleet[discover] for the legacy package)"

HN_API = "https://hn.algolia.com/api/v1/search"
YC_API = "https://api.ycombinator.com/v0.1/companies"
SE_API = "https://api.stackexchange.com/2.3"
DEVTO_API = "https://dev.to/api"
LEMMY_DEFAULT = "https://programming.dev"
GREENHOUSE_API = "https://boards-api.greenhouse.io/v1/boards/{board}/jobs?content=true"
ASHBY_API = "https://api.ashbyhq.com/posting-api/job-board/{org}"
LEVER_API = "https://api.lever.co/v0/postings/{org}?mode=json"

MAX_BYTES = 2_000_000  # per-fetch response cap
SNIPPET_CHARS = 2000  # snippet fallback / search-hit text cap


class DiscoverError(ValueError):
    pass


@dataclass
class SearchHit:
    url: str
    title: str = ""
    snippet: str = ""
    backend: str = ""


@dataclass
class RawRecord:
    """Unvalidated discovered record; converted to InputItem via to_input_items."""

    text: str
    source_uri: str
    title: str | None = None
    item_id: str | None = None
    metadata: dict[str, Any] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# IDs and URLs
# ---------------------------------------------------------------------------

_ID_BAD = re.compile(r"[^A-Za-z0-9_.-]+")
_WS = re.compile(r"\s+")


def slugify_id(value: str, max_len: int = 128) -> str:
    """Map an arbitrary URL/org string to a valid InputItem id."""
    import hashlib

    slug = _ID_BAD.sub("_", value.strip()).strip("_.")
    slug = re.sub(r"_+", "_", slug) or "item"
    if len(slug) > max_len:
        digest = hashlib.sha256(slug.encode()).hexdigest()[:12]
        slug = f"{slug[: max_len - 13]}_{digest}"
    return slug


def record_id(source_uri: str, title: str | None = None) -> str:
    """Stable, readable id: domain plus title slug (falls back to domain)."""
    base = domain_of(source_uri) or source_uri
    if title and title.strip():
        words = _WS.sub(" ", title.strip()).split(" ")[:8]
        return slugify_id(f"{base}-{'-'.join(words)}")
    return slugify_id(base)


def _stable_suffix(value: str) -> str:
    import hashlib

    return hashlib.sha256(value.encode()).hexdigest()[:8]


def domain_of(url: str) -> str:
    try:
        candidate = str(url).strip()
        parsed = urlparse(candidate if "://" in candidate else f"//{candidate}")
        host = parsed.hostname
        if not host:
            return ""
        host = host.rstrip(".").lower()
        if host.startswith("www."):
            host = host[4:]
        try:
            host = host.encode("idna").decode("ascii")
        except UnicodeError:
            pass
        return host
    except Exception:
        return ""


# Marketing/tracking query params carry no page identity; dropping them keeps
# ?utm_* variants of one page from defeating cross-backend dedupe.
_TRACKING_PARAMS = frozenset({
    "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "utm_id",
    "gclid", "gbraid", "wbraid", "fbclid", "msclkid", "mc_cid", "mc_eid",
    "_hsenc", "_hsmi", "mkt_tok", "vero_conv", "vero_id", "snoobi_campaign",
    "pk_campaign", "pk_kwd", "piwik_campaign", "piwik_kwd", "yclid", "dclid",
})


def canonical_url(url: str) -> str:
    try:
        parts = urlparse(url.strip())
        path = parts.path.rstrip("/")
        query = "&".join(
            part for part in parts.query.split("&")
            if part and part.split("=", 1)[0].lower() not in _TRACKING_PARAMS
        )
        return urlunparse((parts.scheme.lower(), parts.netloc.lower(), path, parts.params, query, ""))
    except Exception:
        return url.strip()


def _safe_json(resp: httpx.Response, url_or_desc: str) -> Any:
    try:
        return resp.json()
    except (ValueError, json.JSONDecodeError) as exc:
        raise DiscoverError(f"invalid JSON response from {url_or_desc}: {exc}") from exc


# ---------------------------------------------------------------------------
# Broad web search backends
# ---------------------------------------------------------------------------

def search_ddgs(query: str, max_results: int = 10) -> list[SearchHit]:
    """Broad metasearch via ddgs (no API key). Requires the discover extra."""
    try:
        from ddgs import DDGS
    except ImportError as exc:
        raise DiscoverError(f"ddgs is not installed ({DISCOVER_EXTRA})") from exc
    hits: list[SearchHit] = []
    try:
        with DDGS() as ddgs:
            for row in ddgs.text(query, max_results=max_results) or []:
                url = (row.get("href") or row.get("url") or "").strip()
                if not url.startswith(("http://", "https://")):
                    continue
                hits.append(SearchHit(
                    url=url,
                    title=(row.get("title") or "")[:500],
                    snippet=(row.get("body") or "")[:SNIPPET_CHARS],
                    backend="ddgs",
                ))
    except DiscoverError:
        raise
    except Exception as exc:
        raise DiscoverError(f"ddgs search failed for {query!r}: {exc}") from exc
    return hits


def search_searxng(
    query: str,
    base_url: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
    max_pages: int = 5,
) -> list[SearchHit]:
    """Broad search via a self-hosted SearXNG instance (``/search?format=json``).

    Paginates (``pageno``) until ``max_results`` hits or an empty page.
    """
    base = base_url.rstrip("/")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        hits: list[SearchHit] = []
        for page in range(1, max_pages + 1):
            params = {"q": query, "format": "json", "language": "en", "pageno": page}
            resp = client.get(f"{base}/search", params=params)  # type: ignore[arg-type]
            if resp.status_code != 200:
                raise DiscoverError(f"SearXNG returned HTTP {resp.status_code} for {query!r}")
            payload = _safe_json(resp, f"SearXNG search for {query!r}")
            page_rows = payload.get("results") or []
            if not page_rows:
                break
            for row in page_rows:
                url = (row.get("url") or "").strip()
                if not url.startswith(("http://", "https://")):
                    continue
                hits.append(SearchHit(
                    url=url,
                    title=(row.get("title") or "")[:500],
                    snippet=(row.get("content") or "")[:SNIPPET_CHARS],
                    backend="searxng",
                ))
                if len(hits) >= max_results:
                    return hits
        return hits
    except DiscoverError:
        raise
    except Exception as exc:
        raise DiscoverError(f"SearXNG search failed for {query!r}: {exc}") from exc
    finally:
        if close:
            client.close()


def search_hn(
    query: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Search Hacker News (Algolia API, no key): stories, hiring threads, comments."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        resp = client.get(HN_API, params={"query": query, "hitsPerPage": max_results})
        if resp.status_code != 200:
            raise DiscoverError(f"HN search returned HTTP {resp.status_code} for {query!r}")
        hits: list[SearchHit] = []
        payload = _safe_json(resp, f"HN search for {query!r}")
        for row in (payload.get("hits") or [])[:max_results]:
            url = (row.get("url") or "").strip() or f"https://news.ycombinator.com/item?id={row.get('objectID')}"
            snippet = (row.get("story_text") or row.get("comment_text") or row.get("title") or "")
            hits.append(SearchHit(
                url=url,
                title=(row.get("title") or "")[:500],
                snippet=_fallback_strip(snippet)[:SNIPPET_CHARS],
                backend="hn",
            ))
        return hits
    except DiscoverError:
        raise
    except Exception as exc:
        raise DiscoverError(f"HN search failed for {query!r}: {exc}") from exc
    finally:
        if close:
            client.close()


# ---------------------------------------------------------------------------
# YC company directory (paginated public JSON API, no key)
# ---------------------------------------------------------------------------

def _yc_get_page(
    page: int,
    client: httpx.Client,
    timeout: float = 20.0,
) -> tuple[list[dict[str, Any]], int]:
    try:
        resp = client.get(YC_API, params={"page": page}, timeout=timeout)
    except Exception as exc:
        raise DiscoverError(f"YC directory fetch failed (page {page}): {exc}") from exc
    if resp.status_code != 200:
        raise DiscoverError(f"YC directory returned HTTP {resp.status_code}")
    payload = _safe_json(resp, f"YC directory page {page}")
    return payload.get("companies") or [], int(payload.get("totalPages") or 1)


def _yc_matches(company: dict[str, Any], words: list[str], batch: str | None, tags: Sequence[str]) -> bool:
    if company.get("status") not in (None, "Active"):
        return False
    if batch and str(company.get("batch") or "").lower() != batch.lower():
        return False
    if tags:
        have = {str(t).lower() for t in (company.get("tags") or []) + (company.get("industries") or [])}
        if not any(t.lower() in have for t in tags):
            return False
    if words:
        haystack = " ".join([
            str(company.get("name") or ""),
            str(company.get("oneLiner") or ""),
            str(company.get("longDescription") or ""),
            " ".join(str(t) for t in (company.get("tags") or [])),
            " ".join(str(t) for t in (company.get("industries") or [])),
        ]).lower()
        if not all(w in haystack for w in words):
            return False
    return True


def _yc_profile_text(company: dict[str, Any]) -> str:
    lines = [
        f"{company.get('name', '')} ({company.get('batch', '')})",
        str(company.get("oneLiner") or ""),
        str(company.get("longDescription") or ""),
        f"Team size: {company.get('teamSize', 'unknown')}",
        f"Industries: {', '.join(company.get('industries') or [])}",
        f"Tags: {', '.join(company.get('tags') or [])}",
        f"Website: {company.get('website') or ''}",
    ]
    return "\n".join(line for line in lines if line.strip())


def _yc_location(company: dict[str, Any]) -> str:
    """Return the most useful human-readable YC headquarters location."""
    for key in ("location", "hqLocation", "hq_location"):
        value = _ats_field_text(company.get(key))
        if value:
            return value
    city = _ats_field_text(company.get("city"))
    country = _ats_field_text(company.get("country"))
    return ", ".join(part for part in (city, country) if part)


def search_yc(
    query: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
    max_pages: int = 10,
) -> list[SearchHit]:
    """Search the YC directory client-side (bulk paginated API, no key).

    Hits point at company websites with the one-liner as snippet: triage
    indicators, not evidence. Use ``fetch --yc`` for full profile records.
    """
    words = [w.lower() for w in query.split() if w.strip()]
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        hits: list[SearchHit] = []
        page, total = 1, 1
        while len(hits) < max_results and page <= min(total, max_pages):
            companies, total = _yc_get_page(page, client, timeout)
            if not companies:
                break
            for company in companies:
                if not _yc_matches(company, words, None, ()):
                    continue
                url = (company.get("website") or company.get("url") or "").strip()
                if not url.startswith(("http://", "https://")):
                    continue
                hits.append(SearchHit(
                    url=url,
                    title=f"{company.get('name', '')} ({company.get('batch', '')})".strip()[:500],
                    snippet=str(company.get("oneLiner") or "")[:SNIPPET_CHARS],
                    backend="yc",
                ))
                if len(hits) >= max_results:
                    return hits
            page += 1
        return hits
    finally:
        if close:
            client.close()


def fetch_yc_companies(
    query: str | None = None,
    batch: str | None = None,
    tags: Sequence[str] = (),
    max_companies: int | None = None,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
    max_pages: int = 20,
    delay: float = 0.2,
) -> list[RawRecord]:
    """Dump YC company profiles as indicator-grade records (directory text, no key).

    The directory is newest-first and large (~250 pages); ``max_pages`` bounds
    the scan, so narrow queries for older companies may need a bigger budget.
    """
    words = [w.lower() for w in (query or "").split() if w.strip()]
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        records: list[RawRecord] = []
        page, total = 1, 1

        def _stamp(records: list[RawRecord]) -> list[RawRecord]:
            # Coverage is load-bearing: the directory is newest-first, so a
            # capped scan may never reach older companies. Every record says
            # how much of the directory was actually seen.
            for record in records:
                record.metadata["yc_pages_scanned"] = page
                record.metadata["yc_total_pages"] = total
            return records

        while page <= min(total, max_pages):
            companies, total = _yc_get_page(page, client, timeout)
            if not companies:
                break
            for company in companies:
                if not _yc_matches(company, words, batch, tags):
                    continue
                records.append(RawRecord(
                    text=_yc_profile_text(company),
                    source_uri=company.get("url") or company.get("website") or "",
                    title=str(company.get("name") or ""),
                    item_id=slugify_id(f"yc-{company.get('slug') or company.get('id')}"),
                    metadata={
                        "source": "ycombinator",
                        "evidence": "profile",
                        "website": company.get("website") or "",
                        "batch": company.get("batch") or "",
                        "tags": list(company.get("tags") or []),
                        "team_size": company.get("teamSize"),
                        "hq_location": _yc_location(company),
                        "timezone": company.get("timezone") or company.get("timeZone") or "",
                    },
                ))
                if max_companies is not None and len(records) >= max_companies:
                    return _stamp(records)
            page += 1
            if delay > 0 and page <= min(total, max_pages):
                time.sleep(delay)
        return _stamp(records)
    finally:
        if close:
            client.close()


def _validate_backends(
    backends: Sequence[str],
    searxng_url: str | None,
    discourse_url: str | None = None,
) -> list[str]:
    unknown = [b for b in backends if b not in BACKENDS]
    if unknown:
        raise DiscoverError(f"unknown search backend(s): {unknown} (choose from {sorted(BACKENDS)})")
    if "searxng" in backends and not searxng_url:
        raise DiscoverError("searxng backend requires --searxng-url (self-hosted instance)")
    if "discourse" in backends and not discourse_url:
        raise DiscoverError("discourse backend requires --discourse-url (instance to search)")
    return list(backends)


def _run_backend(
    backend: str,
    query: str,
    max_results: int,
    searxng_url: str | None,
    client: httpx.Client,
    reddit_subreddits: Sequence[str] = (),
    se_tagged: Sequence[str] = (),
    se_site: str = "stackoverflow",
    discourse_url: str | None = None,
    lemmy_instance: str = LEMMY_DEFAULT,
) -> list[SearchHit]:
    if backend == "ddgs":
        return search_ddgs(query, max_results=max_results)
    if backend == "searxng":
        return search_searxng(query, base_url=searxng_url or "", max_results=max_results, client=client)
    if backend == "yc":
        return search_yc(query, max_results=max_results, client=client)
    if backend == "reddit":
        return search_reddit(query, subreddits=reddit_subreddits, max_results=max_results, client=client)
    if backend == "stackexchange":
        return search_stackexchange(query, tagged=se_tagged, site=se_site,
                                     max_results=max_results, client=client)
    if backend == "discourse":
        return search_discourse(query, base_url=discourse_url or "", max_results=max_results, client=client)
    if backend == "lobsters":
        return search_lobsters(query, max_results=max_results, client=client)
    if backend == "lemmy":
        return search_lemmy(query, instance=lemmy_instance, max_results=max_results, client=client)
    if backend == "devto":
        return search_devto(query, max_results=max_results, client=client)
    if backend == "hn":
        return search_hn(query, max_results=max_results, client=client)
    raise DiscoverError(f"search backend not wired: {backend!r}")


def web_search(
    query: str,
    backends: Sequence[str] = ("ddgs", "hn"),
    max_results: int = 10,
    searxng_url: str | None = None,
    timeout: float = 20.0,
    reddit_subreddits: Sequence[str] = (),
    se_tagged: Sequence[str] = (),
    se_site: str = "stackoverflow",
    discourse_url: str | None = None,
    lemmy_instance: str = LEMMY_DEFAULT,
) -> list[SearchHit]:
    """Run one query across backends; dedupe by canonical URL, keep order."""
    backends = _validate_backends(backends, searxng_url, discourse_url)
    seen: set[str] = set()
    merged: list[SearchHit] = []
    with httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT}) as client:
        for backend in backends:
            hits = _run_backend(backend, query, max_results, searxng_url, client,
                                reddit_subreddits, se_tagged, se_site,
                                discourse_url, lemmy_instance)
            for hit in hits:
                key = canonical_url(hit.url)
                if key in seen:
                    continue
                seen.add(key)
                merged.append(hit)
    return merged


# ---------------------------------------------------------------------------
# Polite fetch + parse
# ---------------------------------------------------------------------------

_robots_cache: dict[str, tuple[list[str], list[str]]] = {}


def _parse_robots(txt: str) -> tuple[list[str], list[str]]:
    """Parse robots.txt rules for our UA group.

    A group is consecutive ``User-agent`` lines plus their rules; it applies
    when any agent line is ``*`` (or names us). A new group starts at a
    ``User-agent`` line following a rule line. Returns (allows, disallows).
    """
    allows: list[str] = []
    disallows: list[str] = []
    applicable = False
    saw_rule = False
    for raw in txt.splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line or ":" not in line:
            continue
        key, _, value = line.partition(":")
        key, value = key.strip().lower(), value.strip()
        if key == "user-agent":
            if saw_rule:
                applicable, saw_rule = False, False
            applicable = applicable or value in (
                "*", "account-fleet-discover", "career-fleet-discover", "fleet-discover"
            )
        elif key in ("allow", "disallow"):
            saw_rule = True
            if applicable and value:
                (allows if key == "allow" else disallows).append(value)
    return allows, disallows


def robots_allowed(url: str, client: httpx.Client, timeout: float = 10.0) -> bool:
    """Minimal robots.txt check with Allow/Disallow longest-match. Fail-open."""
    try:
        parts = urlparse(url)
        origin = f"{parts.scheme}://{parts.netloc}"
        if origin not in _robots_cache:
            try:
                resp = client.get(f"{origin}/robots.txt", timeout=timeout)
                _robots_cache[origin] = _parse_robots(resp.text) if resp.status_code == 200 else ([], [])
            except Exception:
                _robots_cache[origin] = ([], [])
        allows, disallows = _robots_cache[origin]
        path = parts.path or "/"
        longest_allow = max((len(a) for a in allows if path.startswith(a)), default=-1)
        longest_deny = max((len(d) for d in disallows if path.startswith(d)), default=-1)
        return longest_deny < 0 or longest_allow >= longest_deny
    except Exception:
        return True


_CHROME_HINT = re.compile(
    r"nav|menu|footer|header|sidebar|cookie|banner|breadcrumb|social|promo|advert"
    r"|cta|chat|popup|modal|overlay|subscribe|newsletter",
    re.IGNORECASE,
)
_SKIP_ROLES = {"navigation", "banner", "contentinfo", "complementary", "search"}


_VOID_TAGS = {"area", "base", "br", "col", "embed", "hr", "img", "input",
               "link", "meta", "param", "source", "track", "wbr"}


class _FallbackExtractor(HTMLParser):
    """Stdlib boilerplate stripper with browser-like auto-closing (unclosed <p> etc.)."""

    def __init__(self) -> None:
        super().__init__()
        self._chunks: list[str] = []
        self._stack: list[str] = []
        self._skipping = False
        self._skip_level = 0
        self.title = ""

    def _trigger_skip(self, tag: str, attrs: list[tuple[str, str | None]]) -> bool:
        if tag in ("script", "style", "noscript", "nav", "footer", "header", "aside", "form", "head"):
            if tag == "header" and "article" in self._stack:
                return False  # article <header> holds the real title/lede
            return True
        # Content containers outrank chrome heuristics: a "promo" class inside
        # <article>/<main> is page copy, not site chrome.
        if "article" in self._stack or "main" in self._stack:
            return False
        role = dict(attrs).get("role", "")
        if role in _SKIP_ROLES:
            return True
        if tag == "div":
            blob = " ".join([tag] + [k for k, _ in attrs] + [v or "" for _, v in attrs])
            return bool(_CHROME_HINT.search(blob))
        return False

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag in _VOID_TAGS:
            return
        self._stack.append(tag)
        if not self._skipping and self._trigger_skip(tag, attrs):
            self._skipping = True
            self._skip_level = len(self._stack)

    def handle_endtag(self, tag: str) -> None:
        if tag in _VOID_TAGS:
            return  # <img/> style self-closes must not disturb the stack
        while self._stack and self._stack[-1] != tag:
            self._stack.pop()  # implicit close, like browsers
        if self._stack:
            self._stack.pop()
        if self._skipping and len(self._stack) < self._skip_level:
            self._skipping = False

    def handle_data(self, data: str) -> None:
        if not self._skipping and data.strip():
            self._chunks.append(data.strip())

    def get_text(self) -> str:
        return "\n\n".join(self._chunks)


def _remove_hidden_blocks(html: str) -> str:
    """Drop script/style/noscript/template blocks: never page copy.

    The tag-strip rescue path below cannot tell markup from text, so hidden
    blocks must go before any regex strips tags — otherwise raw JSON-LD and
    JS leak into "verbatim source text" and can be cited as evidence.
    """
    return re.sub(
        r"<(script|style|noscript|template)[^>]*>.*?</\1>",
        " ",
        html,
        flags=re.IGNORECASE | re.DOTALL,
    )


def _fallback_strip(html: str) -> str:
    import html as _html

    cleaned = _remove_hidden_blocks(html)
    parser = _FallbackExtractor()
    try:
        parser.feed(cleaned)
        text = parser.get_text()
        return _html.unescape(text) if text else _html.unescape(re.sub(r"<[^>]+>", " ", cleaned))
    except Exception:
        return _html.unescape(re.sub(r"<[^>]+>", " ", cleaned))


def _extract_title(html: str) -> str:
    match = re.search(r"<title[^>]*>(.*?)</title>", html, re.IGNORECASE | re.DOTALL)
    if not match:
        return ""
    import html as _html

    return _WS.sub(" ", _html.unescape(re.sub(r"<[^>]+>", "", match.group(1)))).strip()[:500]


def extract_text(html: str) -> str:
    """HTML -> verbatim plain text. Prefers trafilatura, then readability, then stdlib."""
    if not html.strip():
        return ""
    try:
        import trafilatura  # type: ignore

        out = trafilatura.extract(html, include_comments=False, include_tables=True)
        if out and out.strip():
            return out.strip()
    except ImportError:
        pass
    except Exception:
        pass
    try:
        from readability import Document  # type: ignore

        summary = Document(html).summary()
        if summary and summary.strip():
            text = _fallback_strip(summary)
            if text.strip():
                return text.strip()
    except ImportError:
        pass
    except Exception:
        pass
    return _fallback_strip(html).strip()


def require_playwright() -> None:
    """Fail fast with an install hint when the ``js`` extra is missing."""
    try:
        import playwright  # type: ignore  # noqa: F401
    except ImportError as exc:
        raise DiscoverError(
            "playwright is not installed (pip install career-fleet[js] "
            "&& playwright install chromium; account-fleet[js] also works)"
        ) from exc


def _render_js(url: str, timeout: float = 30.0) -> str:
    """Render a JS-heavy page via Playwright (optional ``js`` extra). Experimental."""
    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError as exc:
        raise DiscoverError(
            "playwright is not installed (pip install career-fleet[js] "
            "&& playwright install chromium; account-fleet[js] also works)"
        ) from exc
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch()
            try:
                page = browser.new_page(user_agent=USER_AGENT)
                page.goto(url, timeout=int(timeout * 1000))
                try:
                    page.wait_for_load_state("networkidle", timeout=8000)
                except Exception:
                    pass
                return page.content()
            finally:
                browser.close()
    except DiscoverError:
        raise
    except Exception as exc:
        raise DiscoverError(f"JS render failed for {url}: {exc}") from exc
def _http_get(
    url: str,
    client: httpx.Client,
    timeout: float,
    respect_robots: bool,
) -> tuple[str, str, bytes]:
    """Single polite GET. Returns (final_url, raw_content_type, capped_bytes)."""
    if respect_robots and not robots_allowed(url, client):
        raise DiscoverError(f"blocked by robots.txt: {url}")
    try:
        resp = client.get(url, timeout=timeout)
    except Exception as exc:
        raise DiscoverError(f"fetch failed for {url}: {exc}") from exc
    if resp.status_code != 200:
        raise DiscoverError(f"HTTP {resp.status_code} for {url}")
    raw_header = resp.headers.get("content-type", "") or ""
    final_url = str(resp.url) if hasattr(resp, "url") else url
    return final_url, raw_header, resp.content[:MAX_BYTES]


def _json_ld_description(html: str) -> str:
    """Pull a description/articleBody out of JSON-LD script blocks.

    Only consults schema.org types that carry article-like content
    (Article, BlogPosting, NewsArticle, JobPosting, Product, WebPage);
    returns the longest candidate or "".
    """
    blocks = re.findall(
        r'<script[^>]+type=["\']application/ld\+json["\'][^>]*>(.*?)</script>',
        html, re.IGNORECASE | re.DOTALL,
    )
    wanted = {"article", "blogposting", "newsarticle", "jobposting", "product", "webpage", "techarticle"}
    best = ""
    for block in blocks:
        try:
            payload = json.loads(block)
        except ValueError:
            continue
        nodes = payload if isinstance(payload, list) else [payload]
        if isinstance(payload, dict) and "@graph" in payload:
            graph = payload["@graph"]
            nodes = graph if isinstance(graph, list) else [graph]
        for node in nodes:
            if not isinstance(node, dict):
                continue
            types = node.get("@type", [])
            types = [types] if isinstance(types, str) else list(types or [])
            if types and not {t.lower() for t in types if isinstance(t, str)} & wanted:
                continue
            for key in ("articleBody", "description", "text"):
                value = node.get(key)
                if isinstance(value, str):
                    value = _WS.sub(" ", value).strip()
                    if len(value) > len(best):
                        best = value
    return best


def _record_from_response(final_url: str, raw_header: str, raw: bytes, source_url: str) -> RawRecord:
    """Parse one fetched response into a verbatim record. Raises DiscoverError."""
    content_type = raw_header.split(";")[0].strip().lower()
    if content_type == "application/pdf" or final_url.lower().endswith(".pdf"):
        text = _extract_pdf_bytes(raw)
        title = domain_of(final_url)
        return RawRecord(text=text, source_uri=final_url, title=title,
                         item_id=record_id(final_url, title),
                         metadata={"evidence": "fetched", "format": "pdf"})
    if content_type == "text/plain" or final_url.endswith(".txt"):
        text = _decode_body(raw, raw_header).strip()
        if not text:
            raise DiscoverError(f"no extractable text for {source_url}")
        return RawRecord(text=text, source_uri=final_url, title=domain_of(final_url),
                         item_id=record_id(final_url), metadata={"evidence": "fetched"})
    if content_type not in ("text/html", "application/xhtml+xml", ""):
        raise DiscoverError(f"unsupported content-type {content_type or 'unknown'} for {source_url}")
    html = _decode_body(raw, raw_header)
    text = extract_text(html)
    structured = ""
    if len(text) < 200:
        # Thin pages (JS shells, boilerplate-stripped bodies) often still
        # carry their content in JSON-LD; prefer it over failing the fetch.
        structured = _json_ld_description(html)
        if len(structured) > len(text):
            text = structured
    if not text:
        raise DiscoverError(f"no extractable text for {source_url}")
    title = _extract_title(html) or domain_of(final_url)
    metadata: dict[str, Any] = {"evidence": "fetched"}
    if structured and text == structured:
        metadata["format"] = "json-ld"
    return RawRecord(text=text, source_uri=final_url, title=title,
                     item_id=record_id(final_url, title),
                     metadata=metadata)
def _decode_body(raw: bytes, content_type: str) -> str:
    """Decode honoring an explicit charset (xml/html default utf-8, text latin-1 per RFC)."""
    match = re.search(r"charset=([\w-]+)", content_type)
    for encoding in ([match.group(1)] if match else []) + ["utf-8", "windows-1252"]:
        try:
            return raw.decode(encoding)
        except (LookupError, UnicodeDecodeError):
            continue
    return raw.decode("utf-8", errors="replace")


def _extract_pdf_bytes(raw: bytes) -> str:
    """Best-effort PDF -> text via pypdf (discover extra), then pdfminer if present."""
    from io import BytesIO

    from .input_data import looks_like_text

    try:
        from pypdf import PdfReader

        reader = PdfReader(BytesIO(raw))
        pages = []
        for page in reader.pages:
            try:
                pages.append(page.extract_text() or "")
            except Exception:
                continue
        text = "\n\n".join(pages).strip()
        # Parser output can still be binary garbage on scanned/odd PDFs;
        # reject it so the next backend (or a clear error) takes over.
        if text and looks_like_text(text):
            return text
    except ImportError:
        pass
    except Exception:
        pass
    try:
        from pdfminer.high_level import (
            extract_text as _pdfminer_extract,
        )

        text = (_pdfminer_extract(BytesIO(raw)) or "").strip()
        if text and looks_like_text(text):
            return text
    except ImportError:
        pass
    except Exception:
        pass
    raise DiscoverError(
        "cannot parse PDF content (install pypdf via "
        f"{DISCOVER_EXTRA}, or download the file for local --input)"
    )


def fetch_text(
    url: str,
    client: httpx.Client | None = None,
    timeout: float = 20.0,
    max_bytes: int = MAX_BYTES,
    respect_robots: bool = True,
    render_js: bool = False,
) -> RawRecord:
    """GET a URL and parse it to a verbatim-text record. Raises DiscoverError."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        if respect_robots and not robots_allowed(url, client):
            raise DiscoverError(f"blocked by robots.txt: {url}")
        if render_js:
            html = _render_js(url, timeout=timeout)[:max_bytes]
            text = extract_text(html)
            if not text:
                raise DiscoverError(f"no extractable text for {url} (even rendered)")
            title = _extract_title(html) or domain_of(url)
            return RawRecord(text=text, source_uri=url, title=title,
                             item_id=record_id(url, title),
                             metadata={"evidence": "fetched", "rendered": "js"})
        final_url, raw_header, raw = _http_get(url, client, timeout, respect_robots=False)
        return _record_from_response(final_url, raw_header, raw[:max_bytes], url)
    finally:
        if close:
            client.close()


# ---------------------------------------------------------------------------
# Community sources: Reddit (Arctic Shift archive + RSS) + Hacker News threads
# ---------------------------------------------------------------------------

ARCTIC_POSTS = "https://arctic-shift.photon-reddit.com/api/posts/search"
ARCTIC_COMMENTS = "https://arctic-shift.photon-reddit.com/api/comments/search"
HN_ITEM_API = "https://hacker-news.firebaseio.com/v0/item/{item_id}.json"

REDDIT_EMPTY = {"", "[removed]", "[deleted]"}
REDDIT_UA = "fleet-discover (+https://github.com/NatesVibeCode/career-fleet; community research)"


def _get_with_backoff(
    client: httpx.Client,
    url: str,
    params: dict[str, Any] | None = None,
    timeout: float = 20.0,
    attempts: int = 4,
) -> httpx.Response:
    """GET with Retry-After/exponential backoff on 429 + Arctic Shift throttle 422s."""
    last_error = ""
    for attempt in range(attempts):
        try:
            resp = client.get(url, params=params, timeout=timeout)
        except Exception as exc:
            last_error = f"request failed for {url}: {exc}"
            resp = None
        if resp is not None:
            if resp.status_code == 200:
                return resp
            body = resp.text[:200].lower()
            retryable = resp.status_code == 429 or (
                resp.status_code == 422 and ("slow down" in body or "timeout" in body)
            )
            if not retryable:
                raise DiscoverError(f"HTTP {resp.status_code} for {url}")
            last_error = f"HTTP {resp.status_code} for {url} (throttled)"
            wait = resp.headers.get("retry-after")
            delay = float(wait) if wait and wait.isdigit() else min(2.0 ** attempt, 30.0)
        else:
            delay = min(2.0 ** attempt, 30.0)
        if attempt < attempts - 1:
            time.sleep(delay)
    raise DiscoverError(f"{last_error} after {attempts} attempts")


def _reddit_post_text(post: dict[str, Any]) -> str:
    title = str(post.get("title") or "").strip()
    selftext = str(post.get("selftext") or "").strip()
    if selftext in REDDIT_EMPTY:
        return title
    return f"{title}\n\n{selftext}".strip()


def _reddit_record(post: dict[str, Any], max_chars: int | None = None) -> RawRecord | None:
    text = _reddit_post_text(post)
    if not text:
        return None
    permalink = str(post.get("permalink") or "")
    url = f"https://www.reddit.com{permalink}" if permalink.startswith("/") else permalink
    subreddit = str(post.get("subreddit") or "")
    return RawRecord(
        text=text[:max_chars] if max_chars else text,
        source_uri=url,
        title=f"r/{subreddit}: {post.get('title') or ''}".strip()[:500],
        item_id=slugify_id(f"reddit-{post.get('id')}"),
        metadata={
            "source": "reddit",
            "evidence": "profile",
            "author": str(post.get("author") or ""),
            "subreddit": subreddit,
            "score": post.get("score"),
            "num_comments": post.get("num_comments"),
        },
    )


def search_reddit(
    query: str,
    subreddits: Sequence[str] = (),
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Full-text subreddit search via the Arctic Shift archive (keyless).

    Hits are triage indicators (title + selftext excerpt). Use ``fetch`` paths
    or Arctic post records for complete text. Respects throttling via backoff.
    """
    words = query.strip()
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": REDDIT_UA})
        close = True
    try:
        subs = [s for s in subreddits] or [""]
        per_sub = max(1, (max_results + len(subs) - 1) // len(subs))
        hits: list[SearchHit] = []
        for sub in subs:
            params: dict[str, Any] = {"query": words, "limit": min(per_sub, 100)}
            if sub:
                params["subreddit"] = sub
            try:
                resp = _get_with_backoff(client, ARCTIC_POSTS, params=params, timeout=timeout)
                posts = _safe_json(resp, ARCTIC_POSTS).get("data") or []
            except DiscoverError:
                continue
            for post in posts:
                if post.get("over_18") in (True, "True", "true"):
                    continue
                permalink = str(post.get("permalink") or "")
                url = f"https://www.reddit.com{permalink}" if permalink.startswith("/") else permalink
                if not url.startswith(("http://", "https://")):
                    continue
                snippet = _reddit_post_text(post)[:SNIPPET_CHARS]
                if not snippet:
                    continue
                hits.append(SearchHit(
                    url=url,
                    title=f"r/{post.get('subreddit')}: {post.get('title') or ''}".strip()[:500],
                    snippet=snippet,
                    backend="reddit",
                ))
                if len(hits) >= max_results:
                    return hits
        return hits
    finally:
        if close:
            client.close()


def fetch_reddit_posts(
    query: str,
    subreddits: Sequence[str] = (),
    max_posts: int | None = None,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Complete Arctic post records (title + full selftext) for a query."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": REDDIT_UA})
        close = True
    try:
        subs = [s for s in subreddits] or [""]
        records: list[RawRecord] = []
        for sub in subs:
            params: dict[str, Any] = {"query": query.strip(), "limit": 100}
            if sub:
                params["subreddit"] = sub
            resp = _get_with_backoff(client, ARCTIC_POSTS, params=params, timeout=timeout)
            for post in _safe_json(resp, ARCTIC_POSTS).get("data") or []:
                if post.get("over_18") in (True, "True", "true"):
                    continue
                record = _reddit_record(post)
                if record is not None:
                    records.append(record)
                if max_posts is not None and len(records) >= max_posts:
                    return records
        return records
    finally:
        if close:
            client.close()


def fetch_reddit_rss(
    subreddit: str,
    sort: str = "new",
    max_results: int = 25,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Fresh subreddit posts via public RSS (selftext HTML included, keyless).

    RSS has no search; it complements Arctic Shift (archive search) with
    freshness. Reddit rate-limits RSS aggressively: backoff is built in.
    """
    import html as _html
    import xml.etree.ElementTree as ET

    if sort not in ("new", "hot", "top", "rising"):
        raise DiscoverError(f"unknown subreddit sort: {sort}")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": REDDIT_UA})
        close = True
    try:
        url = f"https://www.reddit.com/r/{subreddit}/{sort}/.rss"
        resp = _get_with_backoff(client, url, timeout=timeout)
        try:
            root = ET.fromstring(resp.content)
        except Exception as exc:
            raise DiscoverError(f"cannot parse RSS for r/{subreddit}: {exc}") from exc
        ns = {"atom": "http://www.w3.org/2005/Atom"}
        records: list[RawRecord] = []
        for entry in root.findall("atom:entry", ns)[:max_results]:
            title = (entry.findtext("atom:title", default="", namespaces=ns) or "").strip()
            raw_html = entry.findtext("atom:content", default="", namespaces=ns) or ""
            body = _fallback_strip(_html.unescape(raw_html)).strip()
            text = f"{title}\n\n{body}".strip() if body else title
            if not text:
                continue
            link = ""
            link_el = entry.find("atom:link", ns)
            if link_el is not None:
                link = link_el.get("href") or ""
            author = (entry.findtext("atom:author/atom:name", default="", namespaces=ns) or "").strip()
            post_id = ""
            entry_id = (entry.findtext("atom:id", default="", namespaces=ns) or "").strip()
            match = re.search(r"/comments/([a-z0-9]+)/", entry_id) or re.fullmatch(r"t3_([a-z0-9]+)", entry_id)
            if match:
                post_id = match.group(1)
            records.append(RawRecord(
                text=text,
                source_uri=link,
                title=f"r/{subreddit}: {title}".strip()[:500],
                item_id=slugify_id(f"reddit-{post_id or link}"),
                metadata={"source": "reddit-rss", "evidence": "profile",
                          "author": author, "subreddit": subreddit},
            ))
        return records
    finally:
        if close:
            client.close()


def fetch_reddit_thread(
    post_id_or_url: str,
    max_comments: int = 50,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> RawRecord:
    """Full comment thread for a Reddit post via Arctic Shift (keyless)."""
    match = re.search(r"/comments/([A-Za-z0-9]+)(?:[/?#]|$)", post_id_or_url) \
        or re.search(r"redd\.it/([A-Za-z0-9]+)", post_id_or_url, re.IGNORECASE)
    post_id = match.group(1) if match else post_id_or_url.strip()
    if not re.fullmatch(r"[A-Za-z0-9]+", post_id):
        raise DiscoverError(f"not a Reddit post id or comments URL: {post_id_or_url}")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": REDDIT_UA})
        close = True
    try:
        resp = _get_with_backoff(
            client, ARCTIC_COMMENTS,
            params={"link_id": f"t3_{post_id}", "limit": min(max(max_comments * 2, 25), 500)},
            timeout=timeout,
        )
        comments = _safe_json(resp, ARCTIC_COMMENTS).get("data") or []
        kept = [c for c in comments if str(c.get("body") or "").strip() not in REDDIT_EMPTY]
        kept.sort(key=lambda c: int(c.get("score") or 0), reverse=True)
        kept = kept[:max_comments]
        if not kept:
            raise DiscoverError(f"no retrievable comments for Reddit post {post_id}")
        subreddit = str(kept[0].get("subreddit") or "")
        lines = []
        for c in kept:
            author = str(c.get("author") or "unknown")
            body = str(c.get("body") or "").strip()
            score = c.get("score", 0)
            lines.append(f"[{author} (+{score})]: {body}")
        return RawRecord(
            text="\n\n".join(lines),
            source_uri=f"https://www.reddit.com/comments/{post_id}/",
            title=f"Reddit thread r/{subreddit} ({len(kept)} comments)".strip()[:500],
            item_id=slugify_id(f"reddit-thread-{post_id}"),
            metadata={"source": "reddit", "evidence": "profile",
                      "subreddit": subreddit, "post_id": post_id},
        )
    finally:
        if close:
            client.close()

# ---------------------------------------------------------------------------
# Hacker News full threads (official Firebase API, keyless)
# ---------------------------------------------------------------------------

def _hn_clean(text: str) -> str:
    import html as _html

    return _fallback_strip(_html.unescape(text or "")).strip()


def _hn_item(item_id: str, client: httpx.Client, timeout: float) -> dict[str, Any] | None:
    try:
        resp = client.get(HN_ITEM_API.format(item_id=item_id), timeout=timeout)
    except Exception:
        return None
    if resp.status_code != 200:
        return None
    try:
        return _safe_json(resp, HN_ITEM_API.format(item_id=item_id))
    except Exception:
        return None


def fetch_hn_thread(
    item_id_or_url: str,
    max_comments: int = 50,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> RawRecord:
    """Full HN story + top comments via Firebase (keyless). Skips dead/deleted."""
    match = re.search(r"[?&]id=(\d+)", item_id_or_url)
    item_id = match.group(1) if match else item_id_or_url.strip()
    if not item_id.isdigit():
        raise DiscoverError(f"not an HN item id or URL: {item_id_or_url}")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        story = _hn_item(item_id, client, timeout)
        if not story or story.get("type") not in ("story", "poll", "job", "comment", None):
            raise DiscoverError(f"HN item {item_id} not found")
        parts: list[str] = []
        title = str(story.get("title") or f"HN {item_id}")
        parts.append(title)
        selftext = _hn_clean(str(story.get("text") or ""))
        if selftext:
            parts.append(selftext)
        story_url = str(story.get("url") or "")
        if story_url:
            parts.append(story_url)
        queue: list = list(story.get("kids") or [])
        comments: list[str] = []
        while queue and len(comments) < max_comments:
            kid = _hn_item(str(queue.pop(0)), client, timeout)
            if not kid or kid.get("deleted") or kid.get("dead"):
                continue
            body = _hn_clean(str(kid.get("text") or ""))
            if body:
                author = str(kid.get("by") or "unknown")
                comments.append(f"[{author}]: {body}")
            queue.extend(kid.get("kids") or [])
        if comments:
            parts.append(f"{len(comments)} comments:")
            parts.extend(comments)
        text = "\n\n".join(parts).strip()
        if not text:
            raise DiscoverError(f"no retrievable text for HN item {item_id}")
        return RawRecord(
            text=text,
            source_uri=f"https://news.ycombinator.com/item?id={item_id}",
            title=title[:500],
            item_id=slugify_id(f"hn-{item_id}"),
            metadata={"source": "hackernews", "evidence": "profile",
                      "score": story.get("score"), "descendants": story.get("descendants")},
        )
    finally:
        if close:
            client.close()


def _hn_item_id_from_url(url: str) -> str | None:
    if "news.ycombinator.com" not in urlparse(url).netloc.lower():
        return None
    match = re.search(r"[?&]id=(\d+)", url)
    return match.group(1) if match else None


def _reddit_post_id_from_url(url: str) -> str | None:
    netloc = urlparse(url).netloc.lower()
    if netloc.endswith("redd.it"):
        path = urlparse(url).path.strip("/").split("/")[0]
        return path if re.fullmatch(r"[A-Za-z0-9]+", path or "") else None
    if "reddit.com" not in netloc:
        return None
    match = re.search(r"/comments/([A-Za-z0-9]+)(?:[/?#]|$)", url)
    return match.group(1) if match else None


def fetch_smart_url(
    url: str,
    client: httpx.Client | None = None,
    timeout: float = 20.0,
    respect_robots: bool = True,
    render_js: bool = False,
) -> RawRecord:
    """Fetch one URL via the best mechanical path.

    HN item URLs resolve through Firebase (full thread, no scraping);
    Reddit comment URLs resolve through Arctic Shift (login walls defeat HTML
    fetch); everything else uses polite HTML/PDF fetch.
    """
    hn_id = _hn_item_id_from_url(url)
    if hn_id:
        return fetch_hn_thread(hn_id, client=client, timeout=timeout)
    reddit_id = _reddit_post_id_from_url(url)
    if reddit_id:
        return fetch_reddit_thread(reddit_id, client=client, timeout=timeout)
    return fetch_text(url, client=client, timeout=timeout,
                      respect_robots=respect_robots, render_js=render_js)


# ---------------------------------------------------------------------------
# Q&A + forums: Stack Exchange, Discourse, Lobsters, Lemmy, Dev.to (all keyless)
# ---------------------------------------------------------------------------

LAST_SE_QUOTA: dict[str, Any] = {}


def _se_get(
    path: str,
    params: dict[str, Any],
    timeout: float,
    client: httpx.Client,
) -> dict[str, Any]:
    try:
        resp = client.get(f"{SE_API}{path}", params=params, timeout=timeout)
    except Exception as exc:
        raise DiscoverError(f"Stack Exchange request failed: {exc}") from exc
    if resp.status_code != 200:
        raise DiscoverError(f"Stack Exchange HTTP {resp.status_code} ({resp.text[:150]})")
    payload = _safe_json(resp, f"Stack Exchange {path}")
    if "error_id" in payload:
        raise DiscoverError(f"Stack Exchange error {payload.get('error_id')}: {payload.get('error_message')}")
    LAST_SE_QUOTA.update(quota_remaining=payload.get("quota_remaining"), quota_max=payload.get("quota_max"))
    if payload.get("backoff"):
        time.sleep(min(int(payload["backoff"]), 30))
    return payload


def search_stackexchange(
    query: str,
    tagged: Sequence[str] = (),
    site: str = "stackoverflow",
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Question search via api.stackexchange (keyless 300 req/day). Indicator hits."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        params: dict[str, Any] = {"q": query.strip(), "site": site, "pagesize": min(max_results, 100),
                                  "order": "desc", "sort": "relevance"}
        if tagged:
            params["tagged"] = ";".join(tagged)
        payload = _se_get("/search/advanced", params, timeout, client)
        hits: list[SearchHit] = []
        for q in payload.get("items") or []:
            link = str(q.get("link") or "")
            if not link.startswith(("http://", "https://")):
                continue
            title = _hn_clean(str(q.get("title") or ""))
            hits.append(SearchHit(url=link, title=title[:500], snippet=title[:SNIPPET_CHARS],
                                  backend="stackexchange"))
        return hits[:max_results]
    finally:
        if close:
            client.close()


def fetch_stackexchange_questions(
    query: str,
    tagged: Sequence[str] = (),
    site: str = "stackoverflow",
    max_questions: int | None = None,
    include_answers: bool = False,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Full question bodies (+ optional top answer) via api.stackexchange, keyless.

    ``include_answers`` costs one extra API call per question against the
    300 req/day anonymous quota; off by default.
    """
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        params: dict[str, Any] = {"q": query.strip(), "site": site, "filter": "withbody",
                                  "pagesize": min(max_questions or 10, 100),
                                  "order": "desc", "sort": "relevance"}
        if tagged:
            params["tagged"] = ";".join(tagged)
        payload = _se_get("/search/advanced", params, timeout, client)
        records: list[RawRecord] = []
        for q in payload.get("items") or []:
            title = _hn_clean(str(q.get("title") or ""))
            body = extract_text(str(q.get("body") or "")).strip()
            text = f"{title}\n\n{body}".strip() if body else title
            if include_answers:
                answer = _se_top_answer(int(q.get("question_id", 0)), site, timeout, client)
                if answer:
                    text += f"\n\nTop answer (score {answer[1]}):\n{answer[0]}"
            if not text:
                continue
            records.append(RawRecord(
                text=text,
                source_uri=str(q.get("link") or ""),
                title=title[:500],
                item_id=slugify_id(f"se-{site}-{q.get('question_id')}"),
                metadata={"source": "stackexchange", "evidence": "profile", "site": site,
                          "score": q.get("score"), "answer_count": q.get("answer_count"),
                          "tags": list(q.get("tags") or []),
                          "quota_remaining": LAST_SE_QUOTA.get("quota_remaining")},
            ))
            if max_questions is not None and len(records) >= max_questions:
                break
        return records
    finally:
        if close:
            client.close()


def _se_top_answer(question_id: int, site: str, timeout: float, client: httpx.Client) -> tuple[str, Any] | None:
    if not question_id:
        return None
    try:
        payload = _se_get(f"/questions/{question_id}/answers",
                          {"site": site, "filter": "withbody", "pagesize": 1,
                           "order": "desc", "sort": "votes"}, timeout, client)
    except DiscoverError:
        return None
    answers = payload.get("items") or []
    if not answers:
        return None
    body = extract_text(str(answers[0].get("body") or "")).strip()
    return (body, answers[0].get("score")) if body else None


def _discourse_base(base_url: str) -> str:
    base = base_url if "://" in base_url else f"https://{base_url}"
    return base.rstrip("/")


def search_discourse(
    query: str,
    base_url: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Search any Discourse instance (/search.json, keyless). Indicator hits."""
    base = _discourse_base(base_url)
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(f"{base}/search.json", params={"q": query.strip()}, timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"Discourse search failed for {base}: {exc}") from exc
        if resp.status_code != 200:
            raise DiscoverError(f"Discourse search HTTP {resp.status_code} for {base}")
        payload = _safe_json(resp, f"Discourse search for {base}")
        titles = {t.get("id"): _hn_clean(str(t.get("fancy_title") or t.get("title") or ""))
                  for t in payload.get("topics") or []}
        hits: list[SearchHit] = []
        seen_topics: set = set()
        for post in payload.get("posts") or []:
            topic_id = post.get("topic_id")
            if topic_id in seen_topics:
                continue
            seen_topics.add(topic_id)
            title = titles.get(topic_id, "")
            snippet = _fallback_strip(_hn_clean(str(post.get("blurb") or "")))[:SNIPPET_CHARS]
            hits.append(SearchHit(url=f"{base}/t/{topic_id}", title=title[:500],
                                  snippet=snippet or title[:SNIPPET_CHARS], backend="discourse"))
            if len(hits) >= max_results:
                break
        return hits
    finally:
        if close:
            client.close()


def fetch_discourse_topic(
    base_url: str,
    topic_id: str | int,
    max_posts: int = 5,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> RawRecord:
    """Full posts of one Discourse topic (keyless). Skips deleted/small-action posts."""
    import html as _html

    base = _discourse_base(base_url)
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(f"{base}/t/{topic_id}.json", timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"Discourse topic fetch failed ({base}/t/{topic_id}): {exc}") from exc
        if resp.status_code == 404:
            raise DiscoverError(f"Discourse topic not found: {base}/t/{topic_id}")
        if resp.status_code != 200:
            raise DiscoverError(f"Discourse topic HTTP {resp.status_code}: {base}/t/{topic_id}")
        topic = _safe_json(resp, f"Discourse topic {base}/t/{topic_id}")
        title = _html.unescape(str(topic.get("title") or f"Topic {topic_id}"))
        lines = [title]
        count = 0
        for post in (topic.get("post_stream") or {}).get("posts") or []:
            if count >= max_posts:
                break
            if post.get("post_type") != 1 or post.get("deleted_at"):
                continue
            body = extract_text(str(post.get("cooked") or "")).strip()
            if not body:
                continue
            lines.append(f"[{post.get('username', 'unknown')}]: {body}")
            count += 1
        if count == 0:
            raise DiscoverError(f"no retrievable posts in Discourse topic {base}/t/{topic_id}")
        return RawRecord(
            text="\n\n".join(lines),
            source_uri=f"{base}/t/{topic_id}",
            title=title[:500],
            item_id=slugify_id(f"discourse-{urlparse(base).netloc}-{topic_id}"),
            metadata={"source": "discourse", "evidence": "profile",
                      "instance": base, "topic_id": str(topic_id)},
        )
    finally:
        if close:
            client.close()


def fetch_discourse_search(
    base_url: str,
    query: str | None,
    max_topics: int | None = None,
    max_posts_each: int = 5,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> tuple[list[RawRecord], list[dict[str, str]]]:
    """Search (or latest topics without a query) then fetch full topics, keyless."""
    base = _discourse_base(base_url)
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        topic_ids: list = []
        if query and query.strip():
            try:
                resp = client.get(f"{base}/search.json", params={"q": query.strip()}, timeout=timeout)
            except Exception as exc:
                raise DiscoverError(f"Discourse search failed for {base}: {exc}") from exc
            if resp.status_code != 200:
                raise DiscoverError(f"Discourse search HTTP {resp.status_code} for {base}")
            for post in _safe_json(resp, f"Discourse search {base}").get("posts") or []:
                topic_id = post.get("topic_id")
                if topic_id and topic_id not in topic_ids:
                    topic_ids.append(topic_id)
        else:
            try:
                resp = client.get(f"{base}/latest.json", timeout=timeout)
            except Exception as exc:
                raise DiscoverError(f"Discourse latest failed for {base}: {exc}") from exc
            if resp.status_code != 200:
                raise DiscoverError(f"Discourse latest HTTP {resp.status_code} for {base}")
            for topic in (_safe_json(resp, f"Discourse latest {base}").get("topic_list") or {}).get("topics") or []:
                if topic.get("id") and topic["id"] not in topic_ids:
                    topic_ids.append(topic["id"])
        records: list[RawRecord] = []
        skipped: list[dict[str, str]] = []
        for topic_id in topic_ids if max_topics is None else topic_ids[:max_topics]:
            try:
                records.append(fetch_discourse_topic(base, topic_id, max_posts=max_posts_each,
                                                     timeout=timeout, client=client))
            except DiscoverError as exc:
                skipped.append({"source": f"discourse:{base}/t/{topic_id}", "reason": str(exc)})
        return records, skipped
    finally:
        if close:
            client.close()


def fetch_lobsters(
    tag: str | None = None,
    max_results: int = 25,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Lobsters newest (or tag) listing with plain-text descriptions, keyless."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        url = f"https://lobste.rs/t/{tag}.json" if tag else "https://lobste.rs/newest.json"
        try:
            resp = _get_with_backoff(client, url, timeout=timeout)
        except DiscoverError as exc:
            if "HTTP 404" in str(exc):
                raise DiscoverError(f"unknown Lobsters tag: {tag}") from exc
            raise
        records: list[RawRecord] = []
        for story in _safe_json(resp, url) or []:
            title = str(story.get("title") or "").strip()
            description = str(story.get("description_plain") or "").strip()
            text = f"{title}\n\n{description}".strip() if description else title
            if not text:
                continue
            records.append(RawRecord(
                text=text,
                source_uri=str(story.get("comments_url") or story.get("short_id_url") or ""),
                title=title[:500],
                item_id=slugify_id(f"lobsters-{story.get('short_id')}"),
                metadata={"source": "lobsters", "evidence": "profile",
                          "link": str(story.get("url") or ""),
                          "score": story.get("score"), "comment_count": story.get("comment_count"),
                          "tags": list(story.get("tags") or [])},
            ))
            if len(records) >= max_results:
                break
        return records
    finally:
        if close:
            client.close()


def search_lobsters(
    query: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Client-side filter over the Lobsters newest listing (no search API)."""
    words = [w.lower() for w in query.split() if w.strip()]
    own_records = fetch_lobsters(tag=None, max_results=50, timeout=timeout, client=client)
    hits: list[SearchHit] = []
    for rec in own_records:
        if not rec.source_uri.startswith(("http://", "https://")):
            continue
        haystack = f"{rec.title} {rec.text} {' '.join(rec.metadata.get('tags') or [])}".lower()
        if words and not all(w in haystack for w in words):
            continue
        hits.append(SearchHit(url=rec.source_uri, title=rec.title or "",
                              snippet=rec.text[:SNIPPET_CHARS], backend="lobsters"))
        if len(hits) >= max_results:
            break
    return hits


def _lemmy_records(payload: dict[str, Any]) -> list[RawRecord]:
    records: list[RawRecord] = []
    for wrapper in payload.get("posts") or []:
        post = wrapper.get("post") or {}
        if post.get("removed") or post.get("deleted") or post.get("nsfw"):
            continue
        name = str(post.get("name") or "").strip()
        body = str(post.get("body") or "").strip()
        text = f"{name}\n\n{body}".strip() if body else name
        if not text:
            continue
        records.append(RawRecord(
            text=text,
            source_uri=str(post.get("ap_id") or ""),
            title=name[:500],
            item_id=slugify_id(f"lemmy-{post.get('id')}"),
            metadata={"source": "lemmy", "evidence": "profile",
                      "published": str(post.get("published") or "")},
        ))
    for wrapper in payload.get("comments") or []:
        comment = wrapper.get("comment") or {}
        if comment.get("removed") or comment.get("deleted"):
            continue
        body = str(comment.get("content") or "").strip()
        if not body:
            continue
        records.append(RawRecord(
            text=body,
            source_uri=str(comment.get("ap_id") or ""),
            title=body.split("\n", 1)[0][:500],
            item_id=slugify_id(f"lemmy-c-{comment.get('id')}"),
            metadata={"source": "lemmy", "evidence": "profile",
                      "published": str(comment.get("published") or "")},
        ))
    return records


def search_lemmy(
    query: str,
    instance: str = LEMMY_DEFAULT,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Lemmy instance search (posts + comments, keyless). Indicator hits."""
    base = instance.rstrip("/")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(f"{base}/api/v3/search",
                              params={"q": query.strip(), "type_": "All",
                                      "listing_type": "All", "limit": min(max_results, 50)},
                              timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"Lemmy search failed for {base}: {exc}") from exc
        if resp.status_code != 200:
            raise DiscoverError(f"Lemmy search HTTP {resp.status_code} for {base}")
        hits = [SearchHit(url=r.source_uri, title=r.title or "", snippet=r.text[:SNIPPET_CHARS],
                          backend="lemmy")
                for r in _lemmy_records(_safe_json(resp, f"Lemmy search {base}")) if r.source_uri.startswith(("http://", "https://"))]
        return hits[:max_results]
    finally:
        if close:
            client.close()


def fetch_lemmy(
    query: str,
    instance: str = LEMMY_DEFAULT,
    max_results: int = 25,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Full Lemmy post/comment bodies for a query (keyless)."""
    base = instance.rstrip("/")
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(f"{base}/api/v3/search",
                              params={"q": query.strip(), "type_": "All",
                                      "listing_type": "All", "limit": min(max_results, 50)},
                              timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"Lemmy search failed for {base}: {exc}") from exc
        if resp.status_code != 200:
            raise DiscoverError(f"Lemmy search HTTP {resp.status_code} for {base}")
        return _lemmy_records(_safe_json(resp, f"Lemmy search {base}"))[:max_results]
    finally:
        if close:
            client.close()


def fetch_devto_tag(
    tag: str,
    max_articles: int | None = None,
    full_body: bool = True,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Dev.to tag listing (+ full markdown bodies, keyless)."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = _get_with_backoff(client, f"{DEVTO_API}/articles",
                                     params={"tag": tag, "per_page": min(max_articles or 10, 1000)},
                                     timeout=timeout)
        except DiscoverError as exc:
            raise DiscoverError(f"Dev.to listing failed for tag {tag!r}: {exc}") from exc
        records: list[RawRecord] = []
        for article in _safe_json(resp, "Dev.to") or []:
            title = str(article.get("title") or "").strip()
            if full_body:
                try:
                    detail = _get_with_backoff(client, f"{DEVTO_API}/articles/{article.get('id')}", timeout=timeout)
                    body = str(_safe_json(detail, "Dev.to article").get("body_markdown") or "").strip()
                except DiscoverError:
                    body = ""
                text = f"{title}\n\n{body}".strip() if body else title
                evidence = "profile" if body else "indicator"
            else:
                text = f"{title}\n\n{article.get('description') or ''}".strip()
                evidence = "indicator"
            if not text:
                continue
            records.append(RawRecord(
                text=text,
                source_uri=str(article.get("url") or ""),
                title=title[:500],
                item_id=slugify_id(f"devto-{article.get('id')}"),
                metadata={"source": "dev.to", "evidence": evidence,
                          "tags": list(article.get("tag_list") or []),
                          "reactions": (article.get("public_reactions_count") or 0)},
            ))
            if max_articles is not None and len(records) >= max_articles:
                break
        return records
    finally:
        if close:
            client.close()


def search_devto(
    query: str,
    max_results: int = 10,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[SearchHit]:
    """Client-side filter over latest Dev.to articles (no search API)."""
    words = [w.lower() for w in query.split() if w.strip()]
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = _get_with_backoff(client, f"{DEVTO_API}/articles", params={"per_page": 30}, timeout=timeout)
        except DiscoverError as exc:
            raise DiscoverError(f"Dev.to listing failed: {exc}") from exc
        hits: list[SearchHit] = []
        for article in _safe_json(resp, "Dev.to") or []:
            title = str(article.get("title") or "")
            description = str(article.get("description") or "")
            haystack = f"{title} {description} {' '.join(article.get('tag_list') or [])}".lower()
            if words and not all(w in haystack for w in words):
                continue
            url = str(article.get("url") or "")
            if not url.startswith(("http://", "https://")):
                continue
            hits.append(SearchHit(url=url, title=title[:500],
                                  snippet=f"{title}\n\n{description}".strip()[:SNIPPET_CHARS],
                                  backend="devto"))
            if len(hits) >= max_results:
                break
        return hits
    finally:
        if close:
            client.close()


BACKENDS: dict[str, Callable[..., list[SearchHit]]] = {
    "ddgs": search_ddgs,
    "searxng": search_searxng,
    "hn": search_hn,
    "yc": search_yc,
    "reddit": search_reddit,
    "stackexchange": search_stackexchange,
    "discourse": search_discourse,
    "lobsters": search_lobsters,
    "lemmy": search_lemmy,
    "devto": search_devto,
}


# ---------------------------------------------------------------------------
# Structured ATS intake (keyless JSON APIs)
# ---------------------------------------------------------------------------

def _ats_field_text(value: Any) -> str:
    if isinstance(value, dict):
        for key in ("name", "label", "value", "title", "text", "display"):
            if value.get(key):
                value = value[key]
                break
        else:
            value = ""
    if isinstance(value, (list, tuple)):
        return ", ".join(_ats_field_text(item) for item in value if item)
    return str(value or "").strip()


def _ats_location(job: dict[str, Any]) -> str:
    for key in ("location", "locationName", "location_name"):
        location = _ats_field_text(job.get(key))
        if location:
            return location
    categories = job.get("categories")
    if isinstance(categories, dict):
        return _ats_field_text(categories.get("location"))
    return ""


def _ats_timezone(job: dict[str, Any]) -> str:
    for key in ("timezone", "timeZone", "timezoneName"):
        timezone = _ats_field_text(job.get(key))
        if timezone:
            return timezone
    categories = job.get("categories")
    if isinstance(categories, dict):
        return _ats_field_text(categories.get("timezone") or categories.get("timeZone"))
    return ""

def _ats_records(
    jobs: Sequence[dict[str, Any]],
    *,
    source: str,
    get_text: Callable[[dict[str, Any]], str],
    get_url: Callable[[dict[str, Any]], str],
    get_title: Callable[[dict[str, Any]], str],
    get_job_id: Callable[[dict[str, Any]], str],
    org: str,
    max_jobs: int | None = None,
    get_location: Callable[[dict[str, Any]], str] | None = None,
    get_timezone: Callable[[dict[str, Any]], str] | None = None,
) -> list[RawRecord]:
    records: list[RawRecord] = []
    seen_ids: set[str] = set()
    for index, job in enumerate(jobs if max_jobs is None else list(jobs)[:max_jobs]):
        try:
            text = get_text(job)
            source_uri = str(get_url(job) or "").strip()
            title = str(get_title(job) or "").strip()
            raw_job_id = str(get_job_id(job) or "").strip()
        except Exception:
            continue
        if not text or not text.strip():
            continue
        metadata = {"ats": source, "org": org, "evidence": "profile"}
        if get_location:
            try:
                location = get_location(job)
            except Exception:
                location = ""
            if location:
                metadata["location"] = location
        if get_timezone:
            try:
                timezone = get_timezone(job)
            except Exception:
                timezone = ""
            if timezone:
                metadata["timezone"] = timezone
        # Stable identity: never position-dependent, so board reorders keep rerun IDs.
        identity = (
            raw_job_id
            or (f"url-{source_uri}" if source_uri else "")
            or (f"title-{title}" if title else f"text-{_stable_suffix(text)}")
        )
        item_id = slugify_id(f"{org}-{identity}")
        if item_id in seen_ids:
            item_id = slugify_id(f"{item_id}-{_stable_suffix(source_uri or text or str(index))}")
            if item_id in seen_ids:
                item_id = slugify_id(f"{item_id}-{index}")
        seen_ids.add(item_id)
        records.append(RawRecord(
            text=text.strip(),
            source_uri=source_uri,
            title=title,
            item_id=item_id,
            metadata=metadata,
        ))
    return records


def fetch_greenhouse_board(
    board: str,
    max_jobs: int | None = None,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Fetch all postings for a Greenhouse board token (public JSON API, no key)."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(GREENHOUSE_API.format(board=board))
        except Exception as exc:
            raise DiscoverError(f"Greenhouse fetch failed for board {board!r}: {exc}") from exc
        if resp.status_code == 404:
            raise DiscoverError(f"unknown Greenhouse board {board!r}")
        if resp.status_code != 200:
            raise DiscoverError(f"Greenhouse returned HTTP {resp.status_code} for board {board!r}")
        jobs = _safe_json(resp, f"Greenhouse board {board}").get("jobs") or []
        return _ats_records(
            jobs, source="greenhouse", org=board, max_jobs=max_jobs,
            get_text=lambda j: extract_text(j.get("content") or ""),
            get_url=lambda j: j.get("absolute_url") or "",
            get_title=lambda j: str(j.get("title") or ""),
            get_job_id=lambda j: str(j.get("id") or ""),
            get_location=_ats_location,
            get_timezone=_ats_timezone,
        )
    finally:
        if close:
            client.close()


def fetch_ashby_org(
    org: str,
    max_jobs: int | None = None,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Fetch all postings for an Ashby org (public posting API, no key)."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(ASHBY_API.format(org=org))
        except Exception as exc:
            raise DiscoverError(f"Ashby fetch failed for org {org!r}: {exc}") from exc
        if resp.status_code == 404:
            raise DiscoverError(f"unknown Ashby org {org!r}")
        if resp.status_code != 200:
            raise DiscoverError(f"Ashby returned HTTP {resp.status_code} for org {org!r}")
        jobs = _safe_json(resp, f"Ashby org {org}").get("jobs") or []
        return _ats_records(
            [j for j in jobs if j.get("isListed", True)], source="ashby", org=org, max_jobs=max_jobs,
            get_text=lambda j: (j.get("descriptionPlain") or "") or extract_text(j.get("descriptionHtml") or ""),
            get_url=lambda j: j.get("jobUrl") or "",
            get_title=lambda j: str(j.get("title") or ""),
            get_job_id=lambda j: str(j.get("id") or ""),
            get_location=_ats_location,
            get_timezone=_ats_timezone,
        )
    finally:
        if close:
            client.close()


def fetch_lever_org(
    org: str,
    max_jobs: int | None = None,
    timeout: float = 20.0,
    client: httpx.Client | None = None,
) -> list[RawRecord]:
    """Fetch postings for a Lever org. Many companies migrated ATS; 404 is common."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(LEVER_API.format(org=org))
        except Exception as exc:
            raise DiscoverError(f"Lever fetch failed for org {org!r}: {exc}") from exc
        if resp.status_code == 404:
            raise DiscoverError(f"unknown Lever org {org!r} (many companies migrated off Lever)")
        if resp.status_code != 200:
            raise DiscoverError(f"Lever returned HTTP {resp.status_code} for org {org!r}")
        jobs = _safe_json(resp, f"Lever org {org}")
        if isinstance(jobs, dict):
            jobs = jobs.get("postings") or jobs.get("data") or []
        return _ats_records(
            jobs, source="lever", org=org, max_jobs=max_jobs,
            get_text=lambda j: extract_text(
                j.get("descriptionPlain")
                or j.get("description")
                or j.get("descriptionHtml")
                or ""
            ),
            get_url=lambda j: j.get("hostedUrl") or j.get("applyUrl") or "",
            get_title=lambda j: str(j.get("text") or j.get("title") or "")[:200],
            get_job_id=lambda j: str(j.get("id") or ""),
            get_location=_ats_location,
            get_timezone=_ats_timezone,
        )
    finally:
        if close:
            client.close()


# ---------------------------------------------------------------------------
# Sitemaps + same-domain site crawl (no key; honors robots.txt)
# ---------------------------------------------------------------------------

def _sitemap_entries(payload: bytes) -> tuple[list[str], list[tuple[str, str | None]]]:
    """Split a sitemap document into (child_sitemaps, [(page_url, lastmod)]).

    Namespace-blind like the rest of intake; ``lastmod`` is the raw string
    (or None) so recrawl freshness decisions stay downstream.
    """
    import xml.etree.ElementTree as ET

    if payload[:2] == b"\x1f\x8b":
        import gzip

        try:
            payload = gzip.decompress(payload)
        except Exception as exc:
            raise DiscoverError(f"cannot decompress gzipped sitemap: {exc}") from exc
    try:
        root = ET.fromstring(payload)
    except Exception as exc:
        raise DiscoverError(f"cannot parse sitemap XML: {exc}") from exc

    def local(tag: str) -> str:
        return tag.rsplit("}", 1)[-1]

    def _text(node: Any) -> str:
        return (node.text or "").strip()

    sitemaps: list[str] = []
    pages: list[tuple[str, str | None]] = []
    if local(root.tag) == "sitemapindex":
        for child in root:
            if local(child.tag) != "sitemap":
                continue
            for node in child:
                if local(node.tag) == "loc" and _text(node):
                    sitemaps.append(_text(node))
    else:
        for url in root.iter():
            if local(url.tag) != "url":
                continue
            loc, lastmod = "", None
            for node in url:
                kind = local(node.tag)
                if kind == "loc" and _text(node):
                    loc = _text(node)
                elif kind == "lastmod" and _text(node):
                    lastmod = _text(node)
            if loc:
                pages.append((loc, lastmod))
    return sitemaps, pages


def _sitemap_locs(payload: bytes) -> tuple[list[str], list[str]]:
    """Split a sitemap document into (child_sitemaps, page_urls), namespace-blind."""
    sitemaps, entries = _sitemap_entries(payload)
    return sitemaps, [url for url, _ in entries]


def fetch_sitemap_entries(
    sitemap_url: str,
    client: httpx.Client | None = None,
    timeout: float = 20.0,
    max_urls: int | None = None,
    _depth: int = 0,
) -> list[tuple[str, str | None]]:
    """Fetch a sitemap (or sitemapindex, recursively) with lastmod dates.

    Returns ``[(page_url, lastmod)]`` in document order, deduped. ``lastmod``
    is the raw sitemap string or None when absent — freshness policy stays
    with the caller.
    """
    if _depth > 3:
        return []
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(sitemap_url, timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"sitemap fetch failed for {sitemap_url}: {exc}") from exc
        if resp.status_code != 200:
            raise DiscoverError(f"sitemap HTTP {resp.status_code} for {sitemap_url}")
        content = resp.content
        if content[:2] == b"\x1f\x8b":
            import gzip
            try:
                content = gzip.decompress(content)
            except Exception as exc:
                raise DiscoverError(f"cannot decompress gzipped sitemap: {exc}") from exc
        child_maps, entries = _sitemap_entries(content[:MAX_BYTES])
        collected = list(entries)
        for child in child_maps[:10]:
            try:
                collected.extend(fetch_sitemap_entries(child, client=client, timeout=timeout, _depth=_depth + 1))
            except DiscoverError:
                continue  # stale child sitemaps are common; keep the good ones
            if max_urls is not None and len(collected) >= max_urls:
                break
        deduped: list[tuple[str, str | None]] = []
        seen: set[str] = set()
        for url, lastmod in collected:
            if url not in seen:
                seen.add(url)
                deduped.append((url, lastmod))
        return deduped[:max_urls] if max_urls is not None else deduped
    finally:
        if close:
            client.close()


def fetch_sitemap_urls(
    sitemap_url: str,
    client: httpx.Client | None = None,
    timeout: float = 20.0,
    max_urls: int | None = None,
    _depth: int = 0,
) -> list[str]:
    """Fetch a sitemap (or sitemapindex, recursively) and return page URLs."""
    if _depth > 3:
        return []
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        try:
            resp = client.get(sitemap_url, timeout=timeout)
        except Exception as exc:
            raise DiscoverError(f"sitemap fetch failed for {sitemap_url}: {exc}") from exc
        if resp.status_code != 200:
            raise DiscoverError(f"sitemap HTTP {resp.status_code} for {sitemap_url}")
        content = resp.content
        if content[:2] == b"\x1f\x8b":
            import gzip
            try:
                content = gzip.decompress(content)
            except Exception as exc:
                raise DiscoverError(f"cannot decompress gzipped sitemap: {exc}") from exc
        child_maps, pages = _sitemap_locs(content[:MAX_BYTES])
        urls = list(pages)
        for child in child_maps[:10]:
            try:
                urls.extend(fetch_sitemap_urls(child, client=client, timeout=timeout, _depth=_depth + 1))
            except DiscoverError:
                continue  # stale child sitemaps are common; keep the good ones
            if max_urls is not None and len(urls) >= max_urls:
                break
        deduped: list[str] = []
        seen: set[str] = set()
        for u in urls:
            if u not in seen:
                seen.add(u)
                deduped.append(u)
        return deduped[:max_urls] if max_urls is not None else deduped
    finally:
        if close:
            client.close()


def discover_sitemap_url(
    site_url: str,
    client: httpx.Client | None = None,
    timeout: float = 20.0,
) -> str:
    """Locate a site's sitemap via common paths + robots.txt Sitemap: lines."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        parts = urlparse(site_url if "://" in site_url else f"https://{site_url}")
        origin = f"{parts.scheme or 'https'}://{parts.netloc or parts.path}"
        candidates = [f"{origin}/sitemap.xml", f"{origin}/sitemap_index.xml"]
        try:
            resp = client.get(f"{origin}/robots.txt", timeout=timeout)
            if resp.status_code == 200:
                for line in resp.text.splitlines():
                    clean = line.split("#", 1)[0].strip()
                    if clean.lower().startswith("sitemap:"):
                        candidates.append(clean.split(":", 1)[1].strip())
        except Exception:
            pass
        for candidate in candidates:
            try:
                resp = client.get(candidate, timeout=timeout)
            except Exception:
                continue
            if resp.status_code != 200:
                continue
            head = resp.content.lstrip()[:5]
            gzipped = resp.content[:2] == b"\x1f\x8b"
            if gzipped or head.startswith((b"<?xml", b"<urls", b"<sit")):
                return candidate
        raise DiscoverError(f"no sitemap found for {site_url} (tried {len(candidates)} locations)")
    finally:
        if close:
            client.close()


class _LinkExtractor(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.links: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag == "a":
            href = dict(attrs).get("href")
            if href and href.strip():
                self.links.append(href.strip())


def extract_links(html: str, base_url: str) -> list[str]:
    """Same-scheme http(s) links from a page, resolved + defragmented + deduped."""
    from urllib.parse import urldefrag, urljoin

    parser = _LinkExtractor()
    try:
        parser.feed(html)
    except Exception:
        pass
    out, seen = [], set()
    for href in parser.links:
        if href.lower().startswith(("javascript:", "mailto:", "tel:", "data:")):
            continue
        absolute, _ = urldefrag(urljoin(base_url, href))
        if not absolute.startswith(("http://", "https://")):
            continue
        if absolute not in seen:
            seen.add(absolute)
            out.append(absolute)
    return out


def crawl_site(
    start_url: str,
    max_pages: int = 20,
    max_depth: int = 2,
    same_origin: bool = True,
    timeout: float = 20.0,
    delay: float = 1.0,
    respect_robots: bool = True,
    render_js: bool = False,
    client: httpx.Client | None = None,
) -> tuple[list[RawRecord], list[dict[str, str]]]:
    """BFS crawl from one URL: same-origin + depth-capped, polite, robots-aware."""
    close = False
    if client is None:
        client = httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT})
        close = True
    try:
        origin = urlparse(start_url).netloc.lower()
        queue: list[tuple[str, int]] = [(start_url, 0)]
        visited: set[str] = {canonical_url(start_url)}
        records: list[RawRecord] = []
        skipped: list[dict[str, str]] = []
        last_fetch: dict[str, float] = {}
        while queue and len(records) + len(skipped) < max_pages:
            url, depth = queue.pop(0)
            # Per-origin politeness: pace each origin independently so one
            # slow origin neither hammers its server nor throttles the crawl.
            if delay > 0:
                page_origin = urlparse(url).netloc.lower()
                wait = delay - (time.time() - last_fetch.get(page_origin, 0.0))
                if wait > 0:
                    time.sleep(wait)
                last_fetch[page_origin] = time.time()
            try:
                if render_js:
                    if respect_robots and not robots_allowed(url, client):
                        raise DiscoverError(f"blocked by robots.txt: {url}")
                    rendered_html = _render_js(url, timeout=timeout)[:MAX_BYTES]
                    text = extract_text(rendered_html)
                    if not text:
                        raise DiscoverError(f"no extractable text for {url} (even rendered)")
                    title = _extract_title(rendered_html) or domain_of(url)
                    record = RawRecord(text=text, source_uri=url, title=title,
                                       item_id=record_id(url, title),
                                       metadata={"evidence": "fetched", "rendered": "js"})
                    html = rendered_html if depth < max_depth else ""
                else:
                    final_url, raw_header, raw = _http_get(url, client, timeout, respect_robots)
                    record = _record_from_response(final_url, raw_header, raw, url)
                    ctype = raw_header.split(";")[0].strip().lower()
                    html = _decode_body(raw, raw_header) if (
                        depth < max_depth and ctype in ("text/html", "application/xhtml+xml", "")) else ""
                records.append(record)
                if html:
                    for link in extract_links(html, url):
                        if same_origin and urlparse(link).netloc.lower() != origin:
                            continue
                        key = canonical_url(link)
                        if key not in visited:
                            visited.add(key)
                            queue.append((link, depth + 1))
            except DiscoverError as exc:
                skipped.append({"url": url, "reason": str(exc)})
        return records, skipped
    finally:
        if close:
            client.close()


# ---------------------------------------------------------------------------
# Orchestration -> InputItem records
# ---------------------------------------------------------------------------

def to_input_items(records: Sequence[RawRecord], max_chars: int | None = None) -> list[InputItem]:
    """Validate discovered records into closed InputItems.

    Ids are deterministic across processes: collisions get a sha256 suffix of
    the source URI (never the salted builtin hash, which breaks rerun
    identity and --only-ids compounding).
    """
    items: list[InputItem] = []
    seen: set[str] = set()
    for rec in records:
        text = rec.text.strip()
        if not text:
            continue
        if max_chars is not None and len(text) > max_chars:
            text = text[:max_chars]
        item_id = slugify_id(rec.item_id or record_id(rec.source_uri, rec.title))
        if item_id in seen:
            base_id = item_id
            suffix = _stable_suffix(rec.source_uri) if rec.source_uri else _stable_suffix(rec.text)
            item_id = slugify_id(f"{base_id}-{suffix}")
            counter = 1
            while item_id in seen:
                item_id = slugify_id(f"{base_id}-{suffix}-{counter}")
                counter += 1
        seen.add(item_id)
        items.append(InputItem(
            item_id=item_id,
            text=text,
            title=rec.title,
            source_uri=rec.source_uri or None,
            metadata=dict(rec.metadata),
        ))
    return items


def write_items_csv(items: Sequence[InputItem], path: str | Path) -> Path:
    """Write items as accounts.csv (reloadable via load_input_items)."""
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=["item_id", "title", "text", "source_uri"])
        writer.writeheader()
        for item in items:
            writer.writerow({
                "item_id": item.item_id,
                "title": item.title or "",
                "text": item.text,
                "source_uri": item.source_uri or "",
            })
    return out


def write_items_jsonl(items: Sequence[InputItem], path: str | Path) -> Path:
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        for item in items:
            f.write(json.dumps(item.model_dump(mode="json", by_alias=True), ensure_ascii=False) + "\n")
    return out


def run_discovery(
    queries: Sequence[str],
    backends: Sequence[str] = ("ddgs", "hn"),
    max_results: int = 10,
    fetch_full_text: bool = True,
    searxng_url: str | None = None,
    timeout: float = 20.0,
    delay: float = 1.0,
    respect_robots: bool = True,
    max_chars: int | None = None,
    render_js: bool = False,
    reddit_subreddits: Sequence[str] = (),
    se_tagged: Sequence[str] = (),
    se_site: str = "stackoverflow",
    discourse_url: str | None = None,
    lemmy_instance: str = LEMMY_DEFAULT,
) -> tuple[list[InputItem], dict[str, Any]]:
    """Search queries broadly, fetch hits, return (items, report).

    Backend misconfiguration raises immediately; per-hit/per-query failures
    are collected into ``report["skipped"]`` and never abort the run.

    Snippet records (``fetch_full_text=False``) are triage indicators only:
    they carry ``metadata["evidence"] == "indicator"`` and must not back
    tier-1 claims. Re-run without ``--snippets-only`` for grounding-grade
    full text.
    """
    if render_js:
        require_playwright()
    backends = _validate_backends(backends, searxng_url, discourse_url)
    records: list[RawRecord] = []
    skipped: list[dict[str, str]] = []
    hits_seen = 0
    with httpx.Client(timeout=timeout, follow_redirects=True, headers={"User-Agent": USER_AGENT}) as client:
        for query in queries:
            if not query.strip():
                skipped.append({"query": query, "reason": "empty query"})
                continue
            seen_urls: set[str] = set()
            ordered: list[SearchHit] = []
            for backend in backends:
                try:
                    for hit in _run_backend(backend, query, max_results, searxng_url, client,
                                            reddit_subreddits, se_tagged, se_site,
                                            discourse_url, lemmy_instance):
                        key = canonical_url(hit.url)
                        if key in seen_urls:
                            continue
                        seen_urls.add(key)
                        ordered.append(hit)
                except DiscoverError as exc:
                    skipped.append({"query": query, "backend": backend, "reason": str(exc)})
            if not ordered:
                skipped.append({"query": query, "reason": "0 hits from backends"})
            for hit in ordered:
                hits_seen += 1
                if not fetch_full_text:
                    records.append(RawRecord(
                        text=hit.snippet or hit.title,
                        source_uri=hit.url,
                        title=hit.title or None,
                        item_id=record_id(hit.url, hit.title),
                        metadata={
                            "backend": hit.backend,
                            "discovery_backend": hit.backend,
                            "discovery_query": query,
                            "discovered_from": hit.url,
                            "evidence": "indicator",
                        },
                    ))
                    continue
                try:
                    fetched = fetch_smart_url(hit.url, client=client, respect_robots=respect_robots,
                                              render_js=render_js)
                    fetched.metadata.update({
                        "discovery_backend": hit.backend,
                        "discovery_query": query,
                        "discovered_from": hit.url,
                    })
                    records.append(fetched)
                except DiscoverError as exc:
                    skipped.append({"url": hit.url, "reason": str(exc)})
                if delay > 0:
                    time.sleep(delay)
    items = to_input_items(records, max_chars=max_chars)
    return items, {"queries": list(queries), "hits": hits_seen, "items": len(items), "skipped": skipped}
