"""Local SQLite store for career discovery, triage, and evaluated dossiers."""
from __future__ import annotations

import hashlib
import json
import sqlite3
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Dict, List, Optional
from urllib.parse import urlparse


def normalize_domain(value: Optional[str]) -> Optional[str]:
    """Return one stable hostname for company/domain matching."""
    if value is None:
        return None
    candidate = str(value).strip()
    if not candidate:
        return None
    parsed = urlparse(candidate if "://" in candidate else f"//{candidate}")
    host = parsed.hostname
    if not host:
        return candidate.lower().rstrip(".") or None
    host = host.rstrip(".").lower()
    if host.startswith("www."):
        host = host[4:]
    try:
        host = host.encode("idna").decode("ascii")
    except UnicodeError:
        pass
    return host


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

CREATE TABLE IF NOT EXISTS profile_revisions (
    revision_id TEXT PRIMARY KEY,
    profile_kind TEXT NOT NULL CHECK(profile_kind IN ('ideal_company', 'ideal_employer')),
    profile_name TEXT NOT NULL,
    profile_version TEXT NOT NULL,
    profile_json TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS active_profiles (
    profile_kind TEXT PRIMARY KEY CHECK(profile_kind IN ('ideal_company', 'ideal_employer')),
    revision_id TEXT NOT NULL REFERENCES profile_revisions(revision_id),
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS job_postings (
    id TEXT PRIMARY KEY,
    company_id TEXT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    location TEXT,
    timezone TEXT,
    is_remote BOOLEAN DEFAULT 0,
    job_url TEXT,
    raw_text TEXT NOT NULL,
    source_type TEXT,
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
    profile_revision_id TEXT REFERENCES profile_revisions(revision_id),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evals_company ON evaluations(company_id);
CREATE INDEX IF NOT EXISTS idx_evals_lane ON evaluations(lane);
"""


class CareerStore:
    """Manages SQLite database for career intelligence."""

    def __init__(self, db_path: Path | str = "career_fleet.db"):
        db_value = str(db_path)
        self.db_path = Path(db_path).expanduser()
        self._memory_uri: Optional[str] = None
        self._keepalive: Optional[sqlite3.Connection] = None
        if db_value == ":memory:":
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
                for table in ("companies", "job_postings", "evaluations"):
                    columns = {row["name"] for row in con.execute(f"PRAGMA table_info({table})").fetchall()}
                    if table in ("companies", "job_postings") and "timezone" not in columns:
                        con.execute(f"ALTER TABLE {table} ADD COLUMN timezone TEXT")
                    if table == "job_postings" and "source_type" not in columns:
                        con.execute("ALTER TABLE job_postings ADD COLUMN source_type TEXT")
                    if table == "evaluations" and "profile_revision_id" not in columns:
                        con.execute("ALTER TABLE evaluations ADD COLUMN profile_revision_id TEXT")

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
    ) -> str:
        domain = normalize_domain(domain)
        with self.connect() as con:
            with con:
                canonical_id = company_id
                if domain:
                    existing = con.execute(
                        "SELECT id FROM companies WHERE domain = ? AND id != ? LIMIT 1",
                        (domain, company_id),
                    ).fetchone()
                    if existing:
                        # A source can identify the same company by a YC ID,
                        # ATS token, or domain. Reuse the existing canonical
                        # row instead of failing on the unique-domain index.
                        canonical_id = existing["id"]
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
                    (canonical_id, name, domain, stage, headcount, hq_location, timezone, ats_provider, ats_token, website_url, status),
                )
                return canonical_id

    def reset_company_pipeline(
        self,
        company_id: str,
        *,
        clear_postings: bool = False,
        source_type: Optional[str] = None,
    ) -> None:
        """Reset derived funnel state before replacing a company's source data.

        Discovery is a refresh operation. Old postings and lane results must
        not continue to influence the new snapshot.
        """
        with self.connect() as con:
            with con:
                if clear_postings:
                    if source_type:
                        con.execute(
                            "DELETE FROM job_postings WHERE company_id = ? AND source_type = ?",
                            (company_id, source_type),
                        )
                    else:
                        con.execute("DELETE FROM job_postings WHERE company_id = ?", (company_id,))
                con.execute("DELETE FROM evaluations WHERE company_id = ?", (company_id,))
                con.execute(
                    "UPDATE companies SET status = 'discovered', disqualification_reason = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                    (company_id,),
                )

    def replace_company_source_snapshot(
        self,
        company_id: str,
        source_type: str,
        postings: List[Dict[str, Any]],
    ) -> bool:
        """Replace one source's postings in a single transaction.

        Empty snapshots are treated as an unavailable/ambiguous fetch and do
        not erase the last known source data. All rows are validated before
        deleting the existing snapshot, so a bad row cannot leave partial data.
        """
        if not source_type:
            raise ValueError("source_type is required for a source snapshot")
        if not postings:
            return False

        rows = []
        for posting in postings:
            job_id = posting.get("id")
            raw_text = posting.get("raw_text")
            if not job_id or not raw_text:
                raise ValueError("source snapshot postings require non-empty id and raw_text")
            rows.append(
                (
                    str(job_id),
                    str(posting.get("company_id") or company_id),
                    str(posting.get("title") or "Untitled"),
                    posting.get("location"),
                    posting.get("timezone"),
                    1 if posting.get("is_remote") else 0,
                    posting.get("job_url"),
                    str(raw_text),
                    source_type,
                )
            )

        with self.connect() as con:
            with con:
                con.execute(
                    "DELETE FROM job_postings WHERE company_id = ? AND source_type = ?",
                    (company_id, source_type),
                )
                con.execute("DELETE FROM evaluations WHERE company_id = ?", (company_id,))
                con.execute(
                    "UPDATE companies SET status = 'discovered', disqualification_reason = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
                    (company_id,),
                )
                con.executemany(
                    """
                    INSERT INTO job_postings (
                        id, company_id, title, location, timezone, is_remote, job_url, raw_text, source_type, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(id) DO UPDATE SET
                        company_id = excluded.company_id,
                        title = excluded.title,
                        location = excluded.location,
                        timezone = excluded.timezone,
                        is_remote = excluded.is_remote,
                        job_url = excluded.job_url,
                        raw_text = excluded.raw_text,
                        source_type = excluded.source_type
                    """,
                    rows,
                )
        return True

    def list_companies(self, status: Optional[str] = None) -> List[Dict[str, Any]]:
        with self.connect() as con:
            if status:
                rows = con.execute("SELECT * FROM companies WHERE status = ? ORDER BY name ASC", (status,)).fetchall()
            else:
                rows = con.execute("SELECT * FROM companies ORDER BY name ASC").fetchall()
            return [dict(r) for r in rows]

    def get_company_by_domain(self, domain: str) -> Optional[Dict[str, Any]]:
        """Return the canonical company row for a normalized domain."""
        domain = normalize_domain(domain)
        if not domain:
            return None
        with self.connect() as con:
            row = con.execute("SELECT * FROM companies WHERE domain = ?", (domain,)).fetchone()
            return dict(row) if row else None

    def save_profile(self, profile: Any) -> str:
        """Persist an immutable IEP revision and activate it."""
        payload = profile.model_dump(mode="json")
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        revision_id = hashlib.sha256(
            json.dumps(
                {"profile_kind": "ideal_employer", "profile": payload},
                sort_keys=True,
                separators=(",", ":"),
                ensure_ascii=False,
            ).encode("utf-8")
        ).hexdigest()
        with self.connect() as con:
            with con:
                con.execute(
                    """
                    INSERT OR IGNORE INTO profile_revisions (
                        revision_id, profile_kind, profile_name, profile_version, profile_json
                    ) VALUES (?, 'ideal_employer', ?, ?, ?)
                    """,
                    (revision_id, profile.profile_name, profile.version, encoded),
                )
                con.execute(
                    """
                    INSERT INTO active_profiles (profile_kind, revision_id, updated_at)
                    VALUES ('ideal_employer', ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(profile_kind) DO UPDATE SET
                        revision_id = excluded.revision_id,
                        updated_at = CURRENT_TIMESTAMP
                    """,
                    (revision_id,),
                )
        return revision_id

    def load_profile(self) -> Optional[Any]:
        """Load the active IEP from SQLite, if one has been stored."""
        with self.connect() as con:
            row = con.execute(
                """
                SELECT r.profile_json
                FROM active_profiles a
                JOIN profile_revisions r ON r.revision_id = a.revision_id
                WHERE a.profile_kind = 'ideal_employer'
                """
            ).fetchone()
        if not row:
            return None
        from career_fleet.profile import IdealEmployerProfile

        return IdealEmployerProfile.model_validate(json.loads(row["profile_json"]))

    def active_profile_revision_id(self) -> Optional[str]:
        """Return the active IEP revision used for new evaluations."""
        with self.connect() as con:
            row = con.execute(
                "SELECT revision_id FROM active_profiles WHERE profile_kind = 'ideal_employer'"
            ).fetchone()
        return str(row["revision_id"]) if row else None

    @staticmethod
    def _validate_profile_revision(con: sqlite3.Connection, revision_id: Optional[str]) -> None:
        """Keep legacy databases from accepting an unresolvable profile ID."""
        if revision_id is None:
            return
        row = con.execute(
            "SELECT profile_kind FROM profile_revisions WHERE revision_id = ?",
            (revision_id,),
        ).fetchone()
        if row is None:
            raise ValueError(f"profile revision does not exist: {revision_id}")
        if row["profile_kind"] != "ideal_employer":
            raise ValueError(f"profile revision is not an Ideal Employer Profile: {revision_id}")

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
        source_type: Optional[str] = None,
    ) -> None:
        with self.connect() as con:
            with con:
                con.execute(
                    """
                    INSERT INTO job_postings (
                        id, company_id, title, location, timezone, is_remote, job_url, raw_text, source_type, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(id) DO UPDATE SET
                        company_id = excluded.company_id,
                        title = excluded.title,
                        location = excluded.location,
                        timezone = excluded.timezone,
                        is_remote = excluded.is_remote,
                        job_url = excluded.job_url,
                        raw_text = excluded.raw_text,
                        source_type = excluded.source_type
                    """,
                    (job_id, company_id, title, location, timezone, 1 if is_remote else 0, job_url, raw_text, source_type),
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
        profile_revision_id: Optional[str] = None,
    ) -> None:
        quotes_json = json.dumps(quotes or [])
        with self.connect() as con:
            with con:
                self._validate_profile_revision(con, profile_revision_id)
                if lane == "lane2_triage":
                    # A changed hard-filter profile invalidates every later
                    # lane. Keep one current result per lane, not stale gates.
                    con.execute(
                        "DELETE FROM evaluations WHERE company_id = ? AND lane IN ('lane3_systems', 'lane4_culture')",
                        (company_id,),
                    )
                elif lane == "lane3_systems":
                    # Culture depends on the current systems result.
                    con.execute(
                        "DELETE FROM evaluations WHERE company_id = ? AND lane = 'lane4_culture'",
                        (company_id,),
                    )
                con.execute(
                    """
                    INSERT INTO evaluations (
                        id, company_id, lane, status, score, verdict, rationale, quotes_json,
                        model_used, profile_revision_id, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(id) DO UPDATE SET
                        company_id = excluded.company_id,
                        lane = excluded.lane,
                        status = excluded.status,
                        score = excluded.score,
                        verdict = excluded.verdict,
                        rationale = excluded.rationale,
                        quotes_json = excluded.quotes_json,
                        model_used = excluded.model_used,
                        profile_revision_id = excluded.profile_revision_id
                    """,
                    (
                        eval_id, company_id, lane, status, score, verdict, rationale,
                        quotes_json, model_used, profile_revision_id,
                    ),
                )
                # Advance the company through the funnel without allowing a
                # later lane to hide a disqualification.
                if status == "disqualified":
                    con.execute("UPDATE companies SET status = 'disqualified', disqualification_reason = ? WHERE id = ?", (rationale, company_id))
                elif status == "triaged" and lane == "lane2_triage":
                    con.execute(
                        "UPDATE companies SET status = 'triaged', disqualification_reason = NULL WHERE id = ?",
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
                "SELECT id, title, location, timezone, is_remote, job_url, raw_text, source_type FROM job_postings WHERE company_id = ?",
                (company_id,),
            ).fetchall()
            evals = con.execute("SELECT * FROM evaluations WHERE company_id = ? ORDER BY created_at ASC", (company_id,)).fetchall()
            dossier = dict(comp)
            dossier["jobs"] = [dict(j) for j in jobs]
            dossier["evaluations"] = [dict(e) for e in evals]
            return dossier
