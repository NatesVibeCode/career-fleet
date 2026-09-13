"""Local SQLite store for career discovery, triage, and evaluated dossiers."""
from __future__ import annotations

import json
import sqlite3
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Dict, List, Optional


SCHEMA = """
CREATE TABLE IF NOT EXISTS companies (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    domain TEXT,
    stage TEXT,
    headcount INT,
    hq_location TEXT,
    timezone TEXT,
    ats_provider TEXT,
    ats_token TEXT,
    website_url TEXT,
    status TEXT DEFAULT 'discovered',  -- discovered, triaged, qualified, disqualified
    disqualification_reason TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_companies_domain ON companies(domain) WHERE domain IS NOT NULL AND domain != '';
CREATE INDEX IF NOT EXISTS idx_companies_status ON companies(status);

CREATE TABLE IF NOT EXISTS job_postings (
    id TEXT PRIMARY KEY,
    company_id TEXT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    location TEXT,
    timezone TEXT,
    is_remote BOOLEAN DEFAULT 0,
    job_url TEXT,
    raw_text TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_company ON job_postings(company_id);

CREATE TABLE IF NOT EXISTS evaluations (
    id TEXT PRIMARY KEY,
    company_id TEXT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    lane TEXT NOT NULL,             -- lane2_triage, lane3_systems, lane4_culture
    status TEXT NOT NULL,           -- triaged, qualified, disqualified, marginal, error
    score REAL DEFAULT 0.0,
    verdict TEXT,
    rationale TEXT,
    quotes_json TEXT DEFAULT '[]',
    model_used TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evals_company ON evaluations(company_id);
CREATE INDEX IF NOT EXISTS idx_evals_lane ON evaluations(lane);
"""


class CareerStore:
    """Manages SQLite database for career intelligence."""

    def __init__(self, db_path: Path | str = "career_fleet.db"):
        self.db_path = Path(db_path)
        self._memory_uri: Optional[str] = None
        self._keepalive: Optional[sqlite3.Connection] = None
        if str(db_path) == ":memory:":
            # Each sqlite3.connect(":memory:") call creates a different
            # database. A shared in-memory URI plus one keepalive connection
            # makes the store API behave as callers expect.
            self._memory_uri = f"file:career_fleet_{id(self)}?mode=memory&cache=shared"
            self._keepalive = sqlite3.connect(self._memory_uri, uri=True)
        else:
            self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self._init_db()

    @contextmanager
    def connect(self):
        con = sqlite3.connect(self._memory_uri, uri=True) if self._memory_uri else sqlite3.connect(self.db_path)
        con.row_factory = sqlite3.Row
        con.execute("PRAGMA foreign_keys = ON")
        con.execute("PRAGMA journal_mode = WAL")
        try:
            yield con
        finally:
            con.close()

    def close(self) -> None:
        """Release the keepalive connection used by an in-memory store."""
        if self._keepalive is not None:
            self._keepalive.close()
            self._keepalive = None

    def _init_db(self) -> None:
        with self.connect() as con:
            with con:
                con.executescript(SCHEMA)
                # Keep existing user databases usable when new metadata fields
                # are added in a later package version.
                for table in ("companies", "job_postings"):
                    columns = {row["name"] for row in con.execute(f"PRAGMA table_info({table})").fetchall()}
                    if "timezone" not in columns:
                        con.execute(f"ALTER TABLE {table} ADD COLUMN timezone TEXT")

    def upsert_company(
        self,
        company_id: str,
        name: str,
        domain: Optional[str] = None,
        stage: Optional[str] = None,
        headcount: Optional[int] = None,
        hq_location: Optional[str] = None,
        ats_provider: Optional[str] = None,
        ats_token: Optional[str] = None,
        website_url: Optional[str] = None,
        status: str = "discovered",
        timezone: Optional[str] = None,
    ) -> None:
        with self.connect() as con:
            with con:
                con.execute(
                    """
                    INSERT INTO companies (
                        id, name, domain, stage, headcount, hq_location, timezone,
                        ats_provider, ats_token, website_url, status, updated_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(id) DO UPDATE SET
                        name = excluded.name,
                        domain = COALESCE(excluded.domain, companies.domain),
                        stage = COALESCE(excluded.stage, companies.stage),
                        headcount = COALESCE(excluded.headcount, companies.headcount),
                        hq_location = COALESCE(excluded.hq_location, companies.hq_location),
                        timezone = COALESCE(excluded.timezone, companies.timezone),
                        ats_provider = COALESCE(excluded.ats_provider, companies.ats_provider),
                        ats_token = COALESCE(excluded.ats_token, companies.ats_token),
                        website_url = COALESCE(excluded.website_url, companies.website_url),
                        status = excluded.status,
                        updated_at = CURRENT_TIMESTAMP
                    """,
                    (company_id, name, domain, stage, headcount, hq_location, timezone, ats_provider, ats_token, website_url, status),
                )

    def list_companies(self, status: Optional[str] = None) -> List[Dict[str, Any]]:
        with self.connect() as con:
            if status:
                rows = con.execute("SELECT * FROM companies WHERE status = ? ORDER BY name ASC", (status,)).fetchall()
            else:
                rows = con.execute("SELECT * FROM companies ORDER BY name ASC").fetchall()
            return [dict(r) for r in rows]

    def add_job_posting(
        self,
        job_id: str,
        company_id: str,
        title: str,
        raw_text: str,
        location: Optional[str] = None,
        is_remote: bool = False,
        job_url: Optional[str] = None,
        timezone: Optional[str] = None,
    ) -> None:
        with self.connect() as con:
            with con:
                con.execute(
                    """
                    INSERT OR REPLACE INTO job_postings (
                        id, company_id, title, location, timezone, is_remote, job_url, raw_text, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    """,
                    (job_id, company_id, title, location, timezone, 1 if is_remote else 0, job_url, raw_text),
                )

    def record_evaluation(
        self,
        eval_id: str,
        company_id: str,
        lane: str,
        status: str,
        score: float,
        verdict: str,
        rationale: str,
        quotes: Optional[List[str]] = None,
        model_used: Optional[str] = None,
    ) -> None:
        quotes_json = json.dumps(quotes or [])
        with self.connect() as con:
            with con:
                con.execute(
                    """
                    INSERT INTO evaluations (
                        id, company_id, lane, status, score, verdict, rationale, quotes_json, model_used, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(id) DO UPDATE SET
                        company_id = excluded.company_id,
                        lane = excluded.lane,
                        status = excluded.status,
                        score = excluded.score,
                        verdict = excluded.verdict,
                        rationale = excluded.rationale,
                        quotes_json = excluded.quotes_json,
                        model_used = excluded.model_used
                    """,
                    (eval_id, company_id, lane, status, score, verdict, rationale, quotes_json, model_used),
                )
                # Advance the company through the funnel without allowing a
                # later lane to hide a disqualification.
                if status == "disqualified":
                    con.execute("UPDATE companies SET status = 'disqualified', disqualification_reason = ? WHERE id = ?", (verdict, company_id))
                elif status == "triaged" and lane == "lane2_triage":
                    con.execute(
                        "UPDATE companies SET status = 'triaged', disqualification_reason = NULL WHERE id = ? AND status != 'disqualified'",
                        (company_id,),
                    )
                elif lane == "lane3_systems":
                    # Lane 3 is a gate, not the final qualification. Keep the
                    # company in the triaged pool until Lane 4 also passes.
                    con.execute(
                        "UPDATE companies SET status = 'triaged' WHERE id = ? AND status != 'disqualified'",
                        (company_id,),
                    )
                elif lane == "lane4_culture" and status == "qualified":
                    con.execute("UPDATE companies SET status = 'qualified' WHERE id = ? AND status != 'disqualified'", (company_id,))
                elif lane == "lane4_culture" and status != "qualified":
                    con.execute(
                        "UPDATE companies SET status = 'triaged' WHERE id = ? AND status != 'disqualified'",
                        (company_id,),
                    )

    def get_company_dossier(self, company_id: str) -> Optional[Dict[str, Any]]:
        with self.connect() as con:
            comp = con.execute("SELECT * FROM companies WHERE id = ?", (company_id,)).fetchone()
            if not comp:
                return None
            jobs = con.execute(
                "SELECT id, title, location, timezone, is_remote, job_url, raw_text FROM job_postings WHERE company_id = ?",
                (company_id,),
            ).fetchall()
            evals = con.execute("SELECT * FROM evaluations WHERE company_id = ? ORDER BY created_at ASC", (company_id,)).fetchall()
            dossier = dict(comp)
            dossier["jobs"] = [dict(j) for j in jobs]
            dossier["evaluations"] = [dict(e) for e in evals]
            return dossier
