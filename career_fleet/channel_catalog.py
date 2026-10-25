"""Generic, source-bound discovery channels for the standalone Process.

The catalog defines repeatable retrieval routes only. It has no user profile,
fit rules, or implicit company judgments; each Process action records its exact
query, source id, URL, response bytes, and parsing coverage.
"""
from __future__ import annotations

from copy import deepcopy


_WEB_QUERIES = [
    "{objective}",
    "companies {objective}",
    "products and vendors {objective}",
]
_COMMUNITY_QUERIES = [
    "{objective}",
    "companies building {objective}",
]
_DIRECTORY_QUERIES = [
    "{objective}",
    "companies {objective}",
]


def _json(
    source_id: str,
    url: str,
    items: str,
    title: str,
    link: str,
    text: str = "",
    date: str = "",
    name: str = "",
    key: str = "",
    *,
    queries: list[str] | None = None,
    page_size: int = 0,
    item_url_template: str = "",
) -> dict:
    return {
        "id": source_id,
        "kind": "json_mapped",
        "url_template": url,
        "queries": list(queries or _COMMUNITY_QUERIES),
        "pages": 1,
        "page_size": page_size,
        "mapping": {
            "items_path": items,
            "title_path": title,
            "url_path": link,
            "item_url_template": item_url_template,
            "text_path": text,
            "date_path": date,
            "name_path": name,
            "lead_key_path": key,
        },
    }


def _html(
    source_id: str,
    url: str,
    queries: list[str] | None = None,
    *,
    page_size: int = 0,
    external_links_only: bool = False,
) -> dict:
    return {
        "id": source_id,
        "kind": "html_search",
        "url_template": url,
        "queries": list(queries or _WEB_QUERIES),
        "pages": 1,
        "page_size": page_size,
        "external_links_only": external_links_only,
    }


def _rss(source_id: str, url: str, queries: list[str] | None = None) -> dict:
    return {
        "id": source_id,
        "kind": "rss",
        "url_template": url,
        "queries": list(queries or _WEB_QUERIES),
        "pages": 1,
    }


def default_sources(pages: int = 1, enable_browser: bool = False) -> list[dict]:
    """Return the broad generic pack; ``pages`` is caller-selected pagination.

    Non-paginated feeds remain at one response. Page-based APIs use ``page``;
    offset-based search routes translate the same caller page count using their
    declared page size. There is no hidden request or result ceiling here.
    """
    if pages < 1:
        raise ValueError("pages must be at least one")

    sources = [
        # Broad web and news discovery.
        _html("web.duckduckgo", "https://html.duckduckgo.com/html/?q={query}&s={offset}", page_size=30, external_links_only=True),
        _html("web.bing", "https://www.bing.com/search?q={query}&format=html&first={offset}", page_size=10, external_links_only=True),
        _html("web.google", "https://www.google.com/search?q={query}&start={offset}", page_size=10, external_links_only=True),
        _rss("web.bing.rss", "https://www.bing.com/search?format=rss&q={query}"),
        _rss("news.google", "https://news.google.com/rss/search?q={query}&hl=en-US&gl=US&ceid=US:en"),
        _rss("news.bing", "https://www.bing.com/news/search?q={query}&format=rss"),

        # Public product, company, and developer ecosystems.
        _json("hackernews.algolia", "https://hn.algolia.com/api/v1/search?query={query}&page={page}&hitsPerPage=100", "/hits", "/title", "/url", "/title", "/created_at", "/author", "/objectID", page_size=100, item_url_template="https://news.ycombinator.com/item?id={key}"),
        _json("github.repositories", "https://api.github.com/search/repositories?q={query}&page={page}&per_page=100", "/items", "/full_name", "/html_url", "/description", "/created_at", "/owner/login", "/id", page_size=100),
        _json("github.organizations", "https://api.github.com/search/users?q={query}+type:org&page={page}&per_page=100", "/items", "/login", "/html_url", "/type", "/created_at", "/login", "/id", page_size=100),
        _json("gitlab.projects", "https://gitlab.com/api/v4/projects?search={query}&page={page}&per_page=100", "", "/name_with_namespace", "/web_url", "/description", "/created_at", "/namespace/full_path", "/id", page_size=100),
        _json("npm.packages", "https://registry.npmjs.org/-/v1/search?text={query}&size=250&from={offset}", "/objects", "/package/name", "/package/links/npm", "/package/description", "/package/date", "/package/maintainers/0/username", "/package/name", queries=["{objective}", "software {objective}", "developer tools {objective}"], page_size=250),

        # Developer communities and product conversations. These are discovery
        # signals; discussion authors and product names are kept unresolved.
        _json("stackexchange.stackoverflow", "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=stackoverflow&q={query}&page={page}&pagesize=100", "/items", "/title", "/link", "/title", "/creation_date", "/owner/display_name", "/question_id", page_size=100),
        _json("stackexchange.serverfault", "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=serverfault&q={query}&page={page}&pagesize=100", "/items", "/title", "/link", "/title", "/creation_date", "/owner/display_name", "/question_id", page_size=100),
        _json("stackexchange.superuser", "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=superuser&q={query}&page={page}&pagesize=100", "/items", "/title", "/link", "/title", "/creation_date", "/owner/display_name", "/question_id", page_size=100),
        _json("stackexchange.softwareengineering", "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=softwareengineering&q={query}&page={page}&pagesize=100", "/items", "/title", "/link", "/title", "/creation_date", "/owner/display_name", "/question_id", page_size=100),
        _json("stackexchange.askubuntu", "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=askubuntu&q={query}&page={page}&pagesize=100", "/items", "/title", "/link", "/title", "/creation_date", "/owner/display_name", "/question_id", page_size=100),
        _json("forem.articles", "https://dev.to/api/articles/search?q={query}&page={page}&per_page=100", "", "/title", "/url", "/description", "/published_at", "/organization/name", "/id", page_size=100),
        _json("lemmy.programming_dev", "https://programming.dev/api/v3/search?q={query}&type_=All&listing_type=All&limit=50&page={page}", "/posts", "/post/name", "/post/ap_id", "/post/body", "/post/published", "/creator/name", "/post/id", page_size=50),
        _json("lemmy.lemmy_world", "https://lemmy.world/api/v3/search?q={query}&type_=All&listing_type=All&limit=50&page={page}", "/posts", "/post/name", "/post/ap_id", "/post/body", "/post/published", "/creator/name", "/post/id", page_size=50),
        _rss("reddit.search", "https://www.reddit.com/search.rss?q={query}&sort=relevance&t=all", _COMMUNITY_QUERIES),
        _html("lobsters.search", "https://lobste.rs/search?q={query}&page={page}", _COMMUNITY_QUERIES),
        _html("discourse.meta", "https://meta.discourse.org/search?q={query}&page={page}", _COMMUNITY_QUERIES),
        _html("discourse.cloudflare", "https://community.cloudflare.com/search?q={query}&page={page}", _COMMUNITY_QUERIES),
        _html("discourse.python", "https://discuss.python.org/search?q={query}&page={page}", _COMMUNITY_QUERIES),

        # Restore the earlier keyless JSON routes alongside the HTML fallback.
        # The exact source record keys and topic URLs remain source evidence.
        _json(
            "discourse.meta.api",
            "https://meta.discourse.org/search.json?q={query}&page={page}",
            "/topics", "/title", "/__career_fleet_url_missing", "/excerpt",
            "/last_posted_at", "/title", "/id",
            queries=_COMMUNITY_QUERIES, page_size=50,
            item_url_template="https://meta.discourse.org/t/{key}",
        ),
        _json(
            "discourse.cloudflare.api",
            "https://community.cloudflare.com/search.json?q={query}&page={page}",
            "/topics", "/title", "/__career_fleet_url_missing", "/excerpt",
            "/last_posted_at", "/title", "/id",
            queries=_COMMUNITY_QUERIES, page_size=50,
            item_url_template="https://community.cloudflare.com/t/{key}",
        ),
        _json(
            "discourse.python.api",
            "https://discuss.python.org/search.json?q={query}&page={page}",
            "/topics", "/title", "/__career_fleet_url_missing", "/excerpt",
            "/last_posted_at", "/title", "/id",
            queries=_COMMUNITY_QUERIES, page_size=50,
            item_url_template="https://discuss.python.org/t/{key}",
        ),

        # Company directories, software marketplaces, and industry coverage.
        _html("directory.ycombinator", "https://www.ycombinator.com/companies?query={query}", _DIRECTORY_QUERIES),
        _json(
            "directory.ycombinator.api",
            "https://api.ycombinator.com/v0.1/companies?page={page}",
            "/companies", "/name", "/website", "/oneLiner", "", "/name", "/id",
            queries=["{objective}"], page_size=25,
            item_url_template="https://www.ycombinator.com/companies/{key}",
        ),
        _html("marketplace.producthunt", "https://www.producthunt.com/search?q={query}", _DIRECTORY_QUERIES),
        _html("marketplace.wellfound", "https://wellfound.com/startups?query={query}&page={page}", _DIRECTORY_QUERIES, page_size=10),
        _html("marketplace.g2", "https://www.g2.com/search?query={query}", _DIRECTORY_QUERIES),
        _html("directory.capterra", "https://www.capterra.com/search/?query={query}", _DIRECTORY_QUERIES),
        _html("directory.sourceforge", "https://sourceforge.net/directory/?q={query}", ["{objective}", "software {objective}"]),
        _html("directory.alternativeto", "https://alternativeto.net/browse/search/?q={query}", ["{objective}", "software {objective}"]),
        _html("marketplace.shopify_apps", "https://apps.shopify.com/search?q={query}", ["{objective}", "apps {objective}"]),
        _html("marketplace.salesforce", "https://appexchange.salesforce.com/solutions?search={query}", ["{objective}", "apps {objective}"]),
        _html("directory.eu_startups", "https://www.eu-startups.com/?s={query}", _DIRECTORY_QUERIES),
        _html("news.techcrunch", "https://techcrunch.com/?s={query}", _WEB_QUERIES),
        _html("directory.pypi", "https://pypi.org/search/?q={query}", ["{objective}", "python packages {objective}"]),
    ]

    for source in sources:
        source["family"] = source["id"].split(".", 1)[0]
        if source["id"] == "web.bing":
            source["preset"] = "mapped_html"
            source["html_mapping"] = {"item_selector": "li.b_algo", "title_selector": "h2", "url_selector": "h2 a[href]", "text_selector": ".b_caption"}
        if source["id"] == "web.duckduckgo":
            source["readiness"] = {"results_selector": "", "empty_selector": "", "blocked_selector": "#challenge-form, .anomaly-modal", "timeout_ms": 10000}
        if "{page}" in source["url_template"] or "{offset}" in source["url_template"]:
            source["pages"] = pages
    if enable_browser:
        # These public directories commonly render search results in the
        # browser. The source URLs and queries stay fixed in the catalog.
        browser_sources = {
            "marketplace.producthunt",
            "marketplace.wellfound",
            "marketplace.g2",
            "directory.capterra",
            "marketplace.shopify_apps",
            "marketplace.salesforce",
        }
        for source in sources:
            if source["id"] in browser_sources:
                source["kind"] = "browser_html_search"
                source["readiness"] = {"results_selector": "", "empty_selector": "", "blocked_selector": "", "timeout_ms": 10000}
                # Unverified browser mappings have no asserted listing selector;
                # they retain incomplete coverage rather than a working label.
                if source["id"] not in {"marketplace.producthunt", "marketplace.wellfound"}:
                    source.pop("readiness")
            if source["id"] == "marketplace.producthunt":
                source["record_kind"] = "product"
                source["detail_url_template"] = "https://www.producthunt.com/products/{slug}"
                source["readiness"]["results_selector"] = 'button[data-test^="spotlight-result-product-"]'
                source["browser_mapping"] = {
                    "item_selector": 'button[data-test^="spotlight-result-product-"]',
                    "title_selector": "span.text-base.font-semibold",
                    "text_selector": "span.text-sm.font-normal.text-light-gray",
                    "key_attribute": "data-test",
                }
            elif source["id"] == "marketplace.wellfound":
                source["record_kind"] = "organization"
                source["readiness"]["results_selector"] = 'a[href^="/company/"] h2'
                source["browser_mapping"] = {
                    "item_selector": "div.rounded-lg.border.border-gray-400",
                    "title_selector": 'a[href^="/company/"] h2',
                    "url_selector": 'a[href^="/company/"]',
                    "text_selector": "span.text-md.text-neutral-1000",
                }
    return deepcopy(sources)


def source_channels(sources: list[dict]) -> set[str]:
    """Return declared source populations; a Block ID is not a population."""
    return {source.get("family") or source["id"].split(".", 1)[0] for source in sources}
