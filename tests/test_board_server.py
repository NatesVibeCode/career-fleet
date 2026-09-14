"""Tests for Career Fleet Jobs Board and API server."""
from __future__ import annotations

import json
import tempfile
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen

from career_fleet.board import (
    BOARD_HTML_PATH,
    CareerFleetBoardHandler,
    detect_schema_kind,
    load_job_dossiers,
    open_read_only_database,
    resolve_database_path,
)
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


def test_career_research_db_loading():
    research_db = resolve_database_path("career-public-research-worker/career_research.db")
    assert research_db.is_file(), f"career_research.db not found at {research_db}"

    with open_read_only_database(research_db) as conn:
        kind = detect_schema_kind(conn)
        assert kind == "career_research"

    dossiers = load_job_dossiers(research_db)
    assert len(dossiers) > 100, f"Expected >100 companies, got {len(dossiers)}"
    total_jobs = sum(len(d["jobs"]) for d in dossiers)
    assert total_jobs > 2000, f"Expected >2000 jobs, got {total_jobs}"

    # Verify dossier structure complies with normalizeDossiers() in index.html
    first = dossiers[0]
    assert "id" in first
    assert "name" in first
    assert "jobs" in first
    assert "evaluations" in first
    assert "community_signals" in first
    assert isinstance(first["jobs"], list)
    assert isinstance(first["evaluations"], list)
    assert isinstance(first["community_signals"], list)

    job = first["jobs"][0]
    assert "id" in job
    assert "title" in job
    assert "location" in job
    assert "is_remote" in job
    assert "job_url" in job
    assert "raw_text" in job
    assert "source_type" in job


def test_career_fleet_store_loading():
    with tempfile.NamedTemporaryFile(suffix=".db") as f:
        db_path = Path(f.name)
        store = CareerStore(db_path)
        rev_id = store.save_profile(IdealEmployerProfile())
        store.upsert_company("acme-corp", "Acme Corp", domain="acme.example", status="qualified")
        store.add_job_posting(
            "job-42",
            "acme-corp",
            "Staff Systems Engineer",
            location="Remote - US",
            timezone="UTC-8",
            is_remote=True,
            job_url="https://acme.example/jobs/42",
            raw_text="Building distributed event brokers.",
            source_type="greenhouse",
        )
        store.record_evaluation(
            "eval-99",
            "acme-corp",
            "lane3_systems",
            "qualified",
            0.94,
            "HARDENED",
            "Proprietary state machine",
            ["Event-sourced audit log"],
            profile_revision_id=rev_id,
        )

        with open_read_only_database(db_path) as conn:
            kind = detect_schema_kind(conn)
            assert kind == "career_fleet"

        dossiers = load_job_dossiers(db_path)
        assert len(dossiers) == 1
        assert dossiers[0]["name"] == "Acme Corp"
        assert len(dossiers[0]["jobs"]) == 1
        assert dossiers[0]["jobs"][0]["title"] == "Staff Systems Engineer"
        assert len(dossiers[0]["evaluations"]) == 1
        assert dossiers[0]["evaluations"][0]["score"] == 0.94


def test_board_http_server():
    research_db = resolve_database_path("career-public-research-worker/career_research.db")
    assert research_db.is_file()

    CareerFleetBoardHandler.database_target = research_db
    CareerFleetBoardHandler.html_page_content = BOARD_HTML_PATH.read_bytes()

    server = ThreadingHTTPServer(("127.0.0.1", 0), CareerFleetBoardHandler)
    host, port = server.server_address
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    server_thread.start()

    try:
        # Test GET /
        with urlopen(f"http://127.0.0.1:{port}/") as resp:
            assert resp.status == 200
            content_type = resp.headers.get("Content-Type", "")
            assert "text/html" in content_type
            body = resp.read().decode("utf-8")
            assert "Career Fleet Jobs" in body
            assert "Matched jobs" in body

        # Test GET /api/jobs
        with urlopen(f"http://127.0.0.1:{port}/api/jobs") as resp:
            assert resp.status == 200
            content_type = resp.headers.get("Content-Type", "")
            assert "application/json" in content_type
            data = json.loads(resp.read().decode("utf-8"))
            assert isinstance(data, list)
            assert len(data) > 100
            total_roles = sum(len(c["jobs"]) for c in data)
            assert total_roles > 2000

        # Test GET /api/jobs?remote_only=1
        with urlopen(f"http://127.0.0.1:{port}/api/jobs?remote_only=1") as resp:
            assert resp.status == 200
            data = json.loads(resp.read().decode("utf-8"))
            for c in data:
                for j in c["jobs"]:
                    assert j["is_remote"] is True

    finally:
        server.shutdown()
        server.server_close()
