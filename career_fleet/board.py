"""Read-only Career Fleet and Research DB job-board bridge.

Serves the interactive Career Fleet Jobs dashboard and exposes GET /api/jobs
over persistent SQLite databases (either career_fleet.db or career_research.db).
"""
from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import webbrowser
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, quote, urlparse

# Resource paths
BOARD_HTML_PATH = Path(__file__).resolve().parent / "resources" / "board" / "index.html"
OPEN_DESIGN_HTML_PATH = Path(
    "/Users/nate/Library/Application Support/Open Design/namespaces/release-stable/data/projects/b4bfb7f9-394b-4f13-ab21-231140b00c10/index.html"
)


def resolve_board_html_path(custom_path: str | Path | None = None) -> Path:
    """Resolve dashboard HTML path, prioritizing newest edit between Open Design and packaged."""
    if custom_path:
        p = Path(custom_path).expanduser().resolve()
        if p.is_file():
            return p
    env_path = os.environ.get("CAREER_BOARD_HTML")
    if env_path:
        p = Path(env_path).expanduser().resolve()
        if p.is_file():
            return p
    # If Open Design file exists and is newer than packaged, prefer the live design file
    if OPEN_DESIGN_HTML_PATH.is_file():
        if not BOARD_HTML_PATH.is_file() or OPEN_DESIGN_HTML_PATH.stat().st_mtime >= BOARD_HTML_PATH.stat().st_mtime:
            return OPEN_DESIGN_HTML_PATH
    if BOARD_HTML_PATH.is_file():
        return BOARD_HTML_PATH
    if OPEN_DESIGN_HTML_PATH.is_file():
        return OPEN_DESIGN_HTML_PATH
    return BOARD_HTML_PATH


def resolve_database_path(custom_path: str | Path | None = None) -> Path:
    """Resolve database path from argument, environment variable, or known candidate defaults."""
    cwd = Path.cwd().resolve()
    if custom_path:
        p = Path(custom_path).expanduser()
        if p.is_absolute() and p.is_file():
            return p
        if (cwd / p).is_file():
            return (cwd / p).resolve()
        if (cwd.parent / p).is_file():
            return (cwd.parent / p).resolve()
        return (cwd / p).resolve()

    env_val = os.environ.get("CAREER_FLEET_DB") or os.environ.get("CAREER_RESEARCH_DB")
    if env_val:
        p = Path(env_val).expanduser()
        if p.is_absolute() and p.is_file():
            return p
        if (cwd / p).is_file():
            return (cwd / p).resolve()
        if (cwd.parent / p).is_file():
            return (cwd.parent / p).resolve()
        return (cwd / p).resolve()

    # Look for candidates in current directory or parent directories
    candidates = [
        cwd / "career_fleet.db",
        cwd / "career_research.db",
        cwd / "career-public-research-worker" / "career_research.db",
        cwd.parent / "career-public-research-worker" / "career_research.db",
        cwd / "data" / "career_fleet.db",
        cwd.parent / "career-fleet" / "career_fleet.db",
    ]
    for c in candidates:
        if c.is_file():
            return c

    # Default fallback
    return cwd / "career_fleet.db"


def open_read_only_database(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise FileNotFoundError(
            f"SQLite database was not found at {path}. "
            "Pass --db <path> or set CAREER_FLEET_DB before starting."
        )
    uri = f"file:{quote(str(path), safe='/')}?mode=ro"
    connection = sqlite3.connect(uri, uri=True, timeout=5)
    connection.row_factory = sqlite3.Row
    return connection


def parse_json_array(value: object) -> list[object]:
    if isinstance(value, list):
        return value
    if isinstance(value, str) and value:
        try:
            parsed = json.loads(value)
        except (TypeError, ValueError):
            return []
        return parsed if isinstance(parsed, list) else []
    return []


def detect_schema_kind(connection: sqlite3.Connection) -> str:
    """Determine whether the DB matches career_fleet or career_research schema."""
    cursor = connection.cursor()
    tables = {
        row[0]
        for row in cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table'"
        ).fetchall()
    }
    if "evaluations" in tables and "community_signals" in tables:
        return "career_fleet"
    if "job_postings" in tables:
        # Check column names in job_postings
        cols = {
            row[1]
            for row in cursor.execute("PRAGMA table_info(job_postings)").fetchall()
        }
        if "location_text" in cols or "ats_provider" in cols or "raw_description" in cols:
            return "career_research"
        if "raw_text" in cols and "job_url" in cols:
            return "career_fleet"
    return "unknown"


def load_career_fleet_dossiers(
    connection: sqlite3.Connection,
    status_filter: str | None = None,
) -> list[dict[str, Any]]:
    """Load dossiers from a native CareerStore (career_fleet.db)."""
    # Check if qualified companies exist
    has_qualified = connection.execute(
        "SELECT 1 FROM companies WHERE status = 'qualified' LIMIT 1"
    ).fetchone()

    # Determine status filter rule
    if status_filter == "qualified" or (status_filter is None and has_qualified):
        where_clause = "WHERE status = 'qualified'"
    elif status_filter == "all" or not has_qualified:
        where_clause = "WHERE status != 'disqualified'"
    else:
        where_clause = "WHERE status = ?"

    params = (status_filter,) if where_clause == "WHERE status = ?" else ()

    company_rows = connection.execute(
        f"""
        SELECT id, name, domain, stage, headcount, hq_location, timezone,
               website_url, status
        FROM companies
        {where_clause}
        ORDER BY name COLLATE NOCASE ASC
        """,
        params,
    ).fetchall()

    if not company_rows:
        return []

    evaluations = connection.execute(
        """
        SELECT e.id, e.company_id, e.lane, e.status, e.score, e.verdict,
               e.rationale, e.quotes_json, e.model_used,
               e.profile_revision_id, e.created_at
        FROM evaluations AS e
        WHERE NOT EXISTS (
            SELECT 1
            FROM evaluations AS newer
            WHERE newer.company_id = e.company_id
              AND newer.lane = e.lane
              AND (
                COALESCE(newer.created_at, '') > COALESCE(e.created_at, '')
                OR (
                  COALESCE(newer.created_at, '') = COALESCE(e.created_at, '')
                  AND newer.id > e.id
                )
              )
        )
        ORDER BY e.company_id ASC, e.created_at ASC, e.id ASC
        """
    ).fetchall()

    jobs = connection.execute(
        f"""
        SELECT j.id, j.company_id, j.title, j.location, j.timezone,
               j.is_remote, j.job_url, j.raw_text, j.source_type
        FROM job_postings AS j
        INNER JOIN companies AS c ON c.id = j.company_id
        {where_clause.replace('WHERE status', 'WHERE c.status')}
        ORDER BY c.name COLLATE NOCASE ASC, j.title COLLATE NOCASE ASC
        """,
        params,
    ).fetchall()

    signals = connection.execute(
        f"""
        SELECT s.company_id, s.title, s.source_type, s.source_uri,
               s.signal_types_json, s.relevance_score
        FROM community_signals AS s
        INNER JOIN companies AS c ON c.id = s.company_id
        {where_clause.replace('WHERE status', 'WHERE c.status')}
        ORDER BY s.company_id ASC, s.created_at ASC, s.id ASC
        """,
        params,
    ).fetchall()

    dossiers: dict[str, dict[str, Any]] = {}
    for row in company_rows:
        company_id = str(row["id"])
        dossiers[company_id] = {
            "id": company_id,
            "name": row["name"],
            "domain": row["domain"] or "",
            "stage": row["stage"] or "",
            "headcount": row["headcount"],
            "hq_location": row["hq_location"] or "",
            "timezone": row["timezone"] or "",
            "website_url": row["website_url"] or "",
            "status": row["status"] or "",
            "jobs": [],
            "evaluations": [],
            "community_signals": [],
        }

    for row in jobs:
        company = dossiers.get(str(row["company_id"]))
        if company is not None:
            company["jobs"].append(
                {
                    "id": row["id"],
                    "title": row["title"],
                    "location": row["location"] or "Location not provided",
                    "timezone": row["timezone"] or "",
                    "is_remote": bool(row["is_remote"]),
                    "job_url": row["job_url"] or "",
                    "raw_text": row["raw_text"] or "",
                    "source_type": row["source_type"] or "ATS",
                }
            )

    for row in evaluations:
        company = dossiers.get(str(row["company_id"]))
        if company is not None:
            company["evaluations"].append(
                {
                    "id": row["id"],
                    "company_id": row["company_id"],
                    "lane": row["lane"],
                    "status": row["status"],
                    "score": row["score"],
                    "verdict": row["verdict"],
                    "rationale": row["rationale"],
                    "quotes": [
                        q for q in parse_json_array(row["quotes_json"]) if isinstance(q, str)
                    ],
                    "model_used": row["model_used"],
                    "profile_revision_id": row["profile_revision_id"],
                    "created_at": row["created_at"],
                }
            )

    for row in signals:
        company = dossiers.get(str(row["company_id"]))
        if company is not None:
            company["community_signals"].append(
                {
                    "title": row["title"],
                    "source_type": row["source_type"],
                    "source_uri": row["source_uri"],
                    "signal_types": [
                        item
                        for item in parse_json_array(row["signal_types_json"])
                        if isinstance(item, str)
                    ],
                    "relevance_score": row["relevance_score"],
                }
            )

    return list(dossiers.values())


def load_career_research_dossiers(
    connection: sqlite3.Connection,
    active_only: bool = True,
    remote_only: bool = False,
) -> list[dict[str, Any]]:
    """Load dossiers from career-public-research-worker (career_research.db)."""
    # Active jobs filter
    job_filter_clauses = []
    if active_only:
        job_filter_clauses.append("j.is_active = 1")
    if remote_only:
        job_filter_clauses.append("j.workplace_type = 'remote'")

    job_where = f"WHERE {' AND '.join(job_filter_clauses)}" if job_filter_clauses else ""

    # Fetch companies that have matching jobs
    company_query = f"""
        SELECT c.id, c.name, c.domain, c.careers_url, c.description, c.industry,
               c.stage, c.hq_location, c.status
        FROM companies c
        WHERE EXISTS (
            SELECT 1 FROM job_postings j
            WHERE j.company_id = c.id {f"AND {' AND '.join(job_filter_clauses)}" if job_filter_clauses else ""}
        )
        ORDER BY c.name COLLATE NOCASE ASC
    """
    company_rows = connection.execute(company_query).fetchall()
    if not company_rows:
        return []

    # Fetch jobs
    job_query = f"""
        SELECT j.id, j.company_id, j.title, j.location_text, j.workplace_type,
               j.url, j.clean_description, j.raw_description, j.ats_provider,
               j.compensation_text, j.min_comp, j.max_comp, j.currency
        FROM job_postings j
        {job_where}
        ORDER BY j.title COLLATE NOCASE ASC
    """
    job_rows = connection.execute(job_query).fetchall()

    # Check for wave_assessments
    cursor = connection.cursor()
    tables = {
        row[0]
        for row in cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table'"
        ).fetchall()
    }

    waves_by_company: dict[str, sqlite3.Row] = {}
    if "wave_assessments" in tables:
        for w in connection.execute("SELECT * FROM wave_assessments").fetchall():
            waves_by_company[str(w["company_id"])] = w

    # Group jobs by company
    jobs_by_company: dict[str, list[dict[str, Any]]] = {}
    for j in job_rows:
        cid = str(j["company_id"])
        workplace = (j["workplace_type"] or "").lower()
        is_remote = workplace == "remote"
        raw_text = j["clean_description"] or j["raw_description"] or ""

        # Format comp summary if present
        comp_summary = ""
        if j["min_comp"] and j["max_comp"]:
            comp_summary = f"\n\nCompensation: ${j['min_comp']:,.0f} - ${j['max_comp']:,.0f} {j['currency'] or 'USD'}"
        elif j["compensation_text"]:
            comp_summary = f"\n\nCompensation: {j['compensation_text']}"

        jobs_by_company.setdefault(cid, []).append(
            {
                "id": str(j["id"]),
                "title": j["title"] or "Untitled posting",
                "location": j["location_text"] or ("Remote" if is_remote else "Location not specified"),
                "timezone": "",
                "is_remote": is_remote,
                "job_url": j["url"] or "",
                "raw_text": (raw_text + comp_summary).strip(),
                "source_type": (j["ats_provider"] or "ATS").capitalize(),
            }
        )

    dossiers: list[dict[str, Any]] = []
    for c in company_rows:
        cid = str(c["id"])
        evals: list[dict[str, Any]] = []

        w = waves_by_company.get(cid)
        if w:
            wedge_v = w["wedge_verdict"] or ""
            geo_v = w["geographic_receptivity"] or ""
            is_hardened = "HARDENED" in wedge_v
            is_geo_fit = "REMOTE" in geo_v

            evals.append(
                {
                    "id": f"wave-{w['id']}-lane3",
                    "company_id": cid,
                    "lane": "lane3_systems",
                    "status": "qualified" if is_hardened else "disqualified",
                    "score": 0.92 if is_hardened else 0.35,
                    "verdict": wedge_v or "Evaluated",
                    "rationale": w["wedge_rationale"] or "Systems moat evaluated.",
                    "quotes": [w["incumbent_displaced"]] if w["incumbent_displaced"] else [],
                    "model_used": w["model_used"] or "wave-model",
                    "profile_revision_id": "wave-intelligence-v1",
                    "created_at": w["created_at"] or "",
                }
            )
            evals.append(
                {
                    "id": f"wave-{w['id']}-lane4",
                    "company_id": cid,
                    "lane": "lane4_culture",
                    "status": "qualified" if is_geo_fit else "disqualified",
                    "score": 0.90 if is_geo_fit else 0.30,
                    "verdict": geo_v or "Evaluated",
                    "rationale": (
                        (f"Hours: {w['operational_hours_reality']}. " if w["operational_hours_reality"] else "")
                        + (f"GTM blindspot: {w['founder_gtm_blindspot']}" if w["founder_gtm_blindspot"] else "")
                    ) or "Culture & operational hours evaluated.",
                    "quotes": [w["diagnostic_pitch"]] if w["diagnostic_pitch"] else [],
                    "model_used": w["model_used"] or "wave-model",
                    "profile_revision_id": "wave-intelligence-v1",
                    "created_at": w["created_at"] or "",
                }
            )

        website = c["careers_url"] or (f"https://{c['domain']}" if c["domain"] else "")

        dossiers.append(
            {
                "id": cid,
                "name": c["name"] or cid,
                "domain": c["domain"] or "",
                "stage": c["stage"] or "",
                "headcount": None,
                "hq_location": c["hq_location"] or "",
                "timezone": "",
                "website_url": website,
                "status": c["status"] or "active",
                "jobs": jobs_by_company.get(cid, []),
                "evaluations": evals,
                "community_signals": [],
            }
        )

    return dossiers


def load_job_dossiers(
    db_path: Path,
    status_filter: str | None = None,
    remote_only: bool = False,
) -> list[dict[str, Any]]:
    """Universal loader that detects schema and returns normalized dossiers."""
    with open_read_only_database(db_path) as conn:
        kind = detect_schema_kind(conn)
        if kind == "career_fleet":
            return load_career_fleet_dossiers(conn, status_filter=status_filter)
        if kind == "career_research":
            return load_career_research_dossiers(
                conn,
                active_only=(status_filter != "all_including_inactive"),
                remote_only=remote_only,
            )
        # Fallback attempt
        try:
            return load_career_fleet_dossiers(conn, status_filter=status_filter)
        except Exception:
            return load_career_research_dossiers(conn, remote_only=remote_only)


class CareerFleetBoardHandler(SimpleHTTPRequestHandler):
    server_version = "CareerFleetBoard/2.0"
    database_target: Path = Path("career_fleet.db")
    html_page_content: bytes = b""

    def send_json(self, status: int, payload: object) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        parsed_url = urlparse(self.path)
        path = parsed_url.path

        if path in ("/", "/index.html"):
            html_path = resolve_board_html_path()
            if not html_path.is_file():
                self.send_error(404, f"Dashboard HTML resource not found at {html_path}")
                return

            content = html_path.read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Cache-Control", "no-store, no-cache, must-revalidate")
            self.send_header("Content-Length", str(len(content)))
            self.end_headers()
            self.wfile.write(content)
            return

        if path == "/api/jobs":
            params = parse_qs(parsed_url.query)
            status_filter = params.get("status", [None])[0]
            remote_only = params.get("remote_only", ["0"])[0] in ("1", "true", "True")
            try:
                dossiers = load_job_dossiers(
                    self.database_target,
                    status_filter=status_filter,
                    remote_only=remote_only,
                )
                self.send_json(200, dossiers)
            except FileNotFoundError as error:
                self.send_json(503, {"error": "database_unavailable", "message": str(error)})
            except (sqlite3.Error, OSError) as error:
                self.send_json(
                    503,
                    {
                        "error": "database_unavailable",
                        "message": f"Career database could not be read: {error}",
                    },
                )
            return

        # Serve other static files if requested
        super().do_GET()


def run_board_server(
    db_path: Path | str | None = None,
    port: int = 8000,
    host: str = "127.0.0.1",
    open_browser: bool = False,
) -> None:
    resolved_db = resolve_database_path(db_path)
    if not resolved_db.is_file():
        print(f"Error: Persistent database not found at '{resolved_db}'.", file=sys.stderr)
        print("Please check the path or initialize the database first.", file=sys.stderr)
        sys.exit(1)

    if not BOARD_HTML_PATH.is_file():
        print(f"Error: Dashboard HTML not found at {BOARD_HTML_PATH}.", file=sys.stderr)
        sys.exit(1)

    CareerFleetBoardHandler.database_target = resolved_db
    CareerFleetBoardHandler.html_page_content = BOARD_HTML_PATH.read_bytes()

    server_address = (host, port)
    server = ThreadingHTTPServer(server_address, CareerFleetBoardHandler)
    url = f"http://{host}:{port}"
    print(f"✓ Career Fleet Board active at: {url}")
    print(f"  Connected database: {resolved_db}")
    print("  Press Ctrl+C to stop.")

    if open_browser:
        try:
            webbrowser.open(url)
        except Exception:
            pass

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down Career Fleet Board.")
    finally:
        server.server_close()


def main() -> None:
    parser = argparse.ArgumentParser(description="Serve the Career Fleet persistent job board.")
    parser.add_argument("--host", default="127.0.0.1", help="Host to listen on (default: 127.0.0.1)")
    parser.add_argument("--port", type=int, default=8000, help="Port to listen on (default: 8000)")
    parser.add_argument("--db", help="Path to SQLite database (career_fleet.db or career_research.db)")
    parser.add_argument("--open", action="store_true", help="Automatically open the browser")
    args = parser.parse_args()

    run_board_server(
        db_path=args.db,
        port=args.port,
        host=args.host,
        open_browser=args.open,
    )


if __name__ == "__main__":
    main()
