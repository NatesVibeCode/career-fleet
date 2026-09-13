"""Lane 1: Sourcing & Ingestion.
Discovers and onboards target companies and active postings using free_fleet.discover primitives.
"""
from __future__ import annotations

import logging
from typing import Any, Dict, List, Optional
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

    discovered = 0
    jobs_added = 0

    try:
        if source_type == "yc":
            # Target could be batch e.g. "W24" or keyword
            items = fetch_yc_companies(batch=target if target.startswith(("W", "S")) else None, query=target if not target.startswith(("W", "S")) else None, max_items=max_items)
            for it in items:
                cid = it.item_id
                name = it.title or cid
                domain = domain_of(it.source_uri) if it.source_uri else None
                store.upsert_company(
                    company_id=cid,
                    name=name,
                    domain=domain,
                    website_url=it.source_uri,
                    status="discovered",
                )
                store.add_job_posting(
                    job_id=f"job-{cid}-profile",
                    company_id=cid,
                    title=f"{name} - Overview",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    is_remote=True,
                )
                discovered += 1

        elif source_type == "greenhouse":
            items = fetch_greenhouse_board(board=target, max_postings=max_items)
            cid = slugify_id(target)
            store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="greenhouse",
                ats_token=target,
                status="discovered",
            )
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=None,
                    is_remote="remote" in (it.text or "").lower(),
                )
                jobs_added += 1

        elif source_type == "ashby":
            items = fetch_ashby_org(org=target, max_postings=max_items)
            cid = slugify_id(target)
            store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="ashby",
                ats_token=target,
                status="discovered",
            )
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=None,
                    is_remote="remote" in (it.text or "").lower(),
                )
                jobs_added += 1

        elif source_type == "lever":
            items = fetch_lever_org(org=target, max_postings=max_items)
            cid = slugify_id(target)
            store.upsert_company(
                company_id=cid,
                name=target.capitalize(),
                ats_provider="lever",
                ats_token=target,
                status="discovered",
            )
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Unknown Role",
                    raw_text=it.text,
                    job_url=it.source_uri,
                    location=None,
                    is_remote="remote" in (it.text or "").lower(),
                )
                jobs_added += 1

        elif source_type == "site":
            items = crawl_site(origin=target, max_pages=max_items)
            cid = slugify_id(domain_of(target))
            store.upsert_company(
                company_id=cid,
                name=domain_of(target),
                domain=domain_of(target),
                website_url=target,
                status="discovered",
            )
            discovered += 1
            for it in items:
                store.add_job_posting(
                    job_id=it.item_id,
                    company_id=cid,
                    title=it.title or "Site Page",
                    raw_text=it.text,
                    job_url=it.source_uri,
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
        }
    except Exception as exc:
        logger.error(f"Error fetching from {source_type} ({target}): {exc}")
        return {
            "status": "error",
            "source_type": source_type,
            "target": target,
            "error": str(exc),
            "companies_discovered": discovered,
            "postings_added": jobs_added,
        }
