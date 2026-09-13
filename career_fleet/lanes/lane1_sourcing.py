"""Lane 1: Sourcing & Ingestion.
Discovers and onboards target companies and source records using free_fleet.discover primitives.
"""
from __future__ import annotations

import logging
import re
from typing import Any, Dict
from urllib.parse import urlparse
from career_fleet.store import CareerStore

logger = logging.getLogger("career_fleet.lane1")

try:
    from free_fleet.discover import (
        fetch_ashby_org,
        fetch_greenhouse_board,
        fetch_lever_org,
        fetch_yc_companies,
        crawl_site,
        domain_of,
        slugify_id,
    )
    HAS_DISCOVER = True
except ImportError:
    HAS_DISCOVER = False


REMOTE_NEGATION_PATTERN = re.compile(
    r"\b(?:no|not|without)\s+(?:fully\s+)?remote\b|"
    r"\bremote\s+(?:work\s+)?(?:is\s+)?(?:not\s+)?(?:required|allowed|available|permitted|optional)\b",
    re.I,
)
REMOTE_POSITIVE_PATTERN = re.compile(
    r"\b(?:fully|100%|completely|entirely)?\s*remote\b|"
    r"\bremote[- ]first\b|\bwork[- ]from[- ]anywhere\b",
    re.I,
)
REMOTE_ROLE_POSITIVE_PATTERN = re.compile(
    r"\bremote[- ]?(?:role|position|job)\b|"
    r"\b(?:this|the|a|your)\s+(?:role|position|job)\s+(?:is\s+)?(?:fully\s+)?remote\b|"
    r"\b(?:can|may|will)\s+work\s+(?:fully\s+)?remotely\b|"
    r"\bwork\s+from\s+anywhere\b",
    re.I,
)
REMOTE_LOCATION_PATTERN = re.compile(r"\b(?:remote|anywhere)\b", re.I)


def _is_remote_listing(text: str, location: str | None = None) -> bool:
    """Return True only when source text contains positive remote evidence."""
    value = text or ""
    if REMOTE_NEGATION_PATTERN.search(value):
        return False
    if location and not REMOTE_LOCATION_PATTERN.search(location):
        return bool(REMOTE_ROLE_POSITIVE_PATTERN.search(value))
    return bool(REMOTE_POSITIVE_PATTERN.search(value))


def _item_metadata(item: Any, key: str) -> str | None:
    metadata = getattr(item, "metadata", {}) or {}
    value = metadata.get(key)
    return str(value).strip() if value is not None else None


def _item_int_metadata(item: Any, key: str) -> int | None:
    value = _item_metadata(item, key)
    if not value:
        return None
    try:
        return int(value.replace(",", ""))
    except (TypeError, ValueError):
        return None


def _item_domain(item: Any) -> str | None:
    # YC's source URI is often the shared directory page. It is not a
    # company domain and must not be used as a unique-domain fallback.
    website = _item_metadata(item, "website")
    if not website:
        return None
    candidate = str(website).strip()
    if "://" not in candidate:
        candidate = f"https://{candidate}"
    return domain_of(candidate) or None


def run_lane1_sourcing(
    store: CareerStore,
    source_type: str,
    target: str,
    max_items: int = 50,
) -> Dict[str, Any]:
    """Discover companies or postings and record them in the store.
    
    source_type: 'yc', 'greenhouse', 'ashby', 'lever', or 'site'
    target: batch/tag for YC, board token for ATS, or URL for site crawl.
    """
    if not HAS_DISCOVER:
        raise RuntimeError("free_fleet.discover is required. Install with pip install 'career-fleet[discover]'.")
    if max_items < 1:
        raise ValueError("max_items must be at least 1")

    discovered = 0
    jobs_added = 0
    pages_skipped = 0
    refreshed_companies: set[tuple[str, str]] = set()

    def refresh_company(company_id: str) -> None:
        key = (company_id, source_type)
        if key not in refreshed_companies:
            store.reset_company_pipeline(company_id, clear_postings=True, source_type=source_type)
            refreshed_companies.add(key)

    try:
        if source_type == "yc":
            # Target could be batch e.g. "W24" or keyword
            normalized_target = target.strip()
            is_batch = bool(re.fullmatch(r"[WSws]\d+", normalized_target))
            items = fetch_yc_companies(
                batch=normalized_target.upper() if is_batch else None,
                query=None if is_batch else normalized_target,
                max_companies=max_items,
            )
            for it in items:
                source_record_id = it.item_id
                cid = source_record_id
                name = it.title or cid
                website = _item_metadata(it, "website") or it.source_uri
                cid = store.upsert_company(
                    company_id=cid,
                    name=name,
                    domain=_item_domain(it),
                    headcount=_item_int_metadata(it, "team_size"),
                    hq_location=_item_metadata(it, "hq_location") or _item_metadata(it, "location"),
                    timezone=_item_metadata(it, "timezone"),
                    website_url=website,
                    status="discovered",
                )
                refresh_company(cid)
                store.add_job_posting(
                    job_id=f"job-{source_record_id}-profile",
                    company_id=cid,
                    title=f"{name} - Overview",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    timezone=_item_metadata(it, "timezone"),
                    is_remote=_is_remote_listing(it.text),
                    source_type=source_type,
                )
                discovered += 1

        elif source_type == "greenhouse":
            items = fetch_greenhouse_board(board=target, max_jobs=max_items)
            cid = slugify_id(target)
            cid = store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="greenhouse",
                ats_token=target,
                status="discovered",
            )
            refresh_company(cid)
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=_item_metadata(it, "location"),
                    timezone=_item_metadata(it, "timezone"),
                    is_remote=_is_remote_listing(it.text, _item_metadata(it, "location")),
                    source_type=source_type,
                )
                jobs_added += 1

        elif source_type == "ashby":
            items = fetch_ashby_org(org=target, max_jobs=max_items)
            cid = slugify_id(target)
            cid = store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="ashby",
                ats_token=target,
                status="discovered",
            )
            refresh_company(cid)
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=_item_metadata(it, "location"),
                    timezone=_item_metadata(it, "timezone"),
                    is_remote=_is_remote_listing(it.text, _item_metadata(it, "location")),
                    source_type=source_type,
                )
                jobs_added += 1

        elif source_type == "lever":
            items = fetch_lever_org(org=target, max_jobs=max_items)
            cid = slugify_id(target)
            cid = store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="lever",
                ats_token=target,
                status="discovered",
            )
            refresh_company(cid)
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=_item_metadata(it, "location"),
                    timezone=_item_metadata(it, "timezone"),
                    is_remote=_is_remote_listing(it.text, _item_metadata(it, "location")),
                    source_type=source_type,
                )
                jobs_added += 1

        elif source_type == "site":
            parsed_target = urlparse(target)
            site_domain = domain_of(target)
            if parsed_target.scheme not in ("http", "https") or not site_domain:
                raise ValueError("site target must be an absolute http:// or https:// URL with a host")
            items, skipped = crawl_site(start_url=target, max_pages=max_items)
            pages_skipped = len(skipped)
            existing = store.get_company_by_domain(site_domain)
            cid = existing["id"] if existing else slugify_id(site_domain)
            cid = store.upsert_company(
                company_id=cid,
                name=existing["name"] if existing else site_domain,
                domain=site_domain,
                website_url=target,
                status="discovered",
            )
            refresh_company(cid)
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Site Page",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=_item_metadata(it, "location"),
                    timezone=_item_metadata(it, "timezone"),
                    is_remote=_is_remote_listing(it.text, _item_metadata(it, "location")),
                    source_type=source_type,
                )
                jobs_added += 1

        else:
            raise ValueError(f"Unknown source_type '{source_type}'. Choose from 'yc', 'greenhouse', 'ashby', 'lever', 'site'.")

        return {
            "status": "success",
            "source_type": source_type,
            "target": target,
            "companies_discovered": discovered,
            "postings_added": jobs_added,
            "pages_skipped": pages_skipped,
        }
    except Exception as exc:
        error = str(exc)
        if "career-fleet" not in error:
            error = error.replace("account-fleet", "career-fleet")
        logger.error(f"Error fetching from {source_type} ({target}): {error}")
        return {
            "status": "error",
            "source_type": source_type,
            "target": target,
            "error": error,
            "companies_discovered": discovered,
            "postings_added": jobs_added,
            "pages_skipped": pages_skipped,
        }
