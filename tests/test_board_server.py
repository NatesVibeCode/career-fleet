"""Tests for Career Fleet Jobs Board and API server."""
from __future__ import annotations

import json
import os
import tempfile
import threading
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen

import pytest

from career_fleet.board import (
    CareerFleetBoardHandler,
    detect_schema_kind,
    load_job_dossiers,
    open_read_only_database,
    resolve_database_path,
    resolve_research_database_path,
)
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


def _local_worker_database() -> Path | None:
    """The local worker database, when this checkout has one.

    These tests assert real content (>100 companies, real jobs) in the author's
    career-public-research-worker database. A fresh clone and CI do not have it,
    so they skip there instead of failing on a missing file.
    """
    try:
        path = resolve_database_path("career-public-research-worker/career_research.db")
    except Exception:
        return None
    return path if path.is_file() else None


@pytest.fixture
def local_worker_db() -> Path:
    path = _local_worker_database()
    if path is None:
        pytest.skip("needs a local career-public-research-worker/career_research.db; not present in CI")
    return path


@pytest.fixture(autouse=True)
def _restore_handler_targets():
    """Handler targets are class attributes, so a test must not leak them."""
    saved = (
        CareerFleetBoardHandler.database_target,
        CareerFleetBoardHandler.research_database_target,
    )
    yield
    CareerFleetBoardHandler.database_target = saved[0]
    CareerFleetBoardHandler.research_database_target = saved[1]


def test_career_research_db_loading(local_worker_db):
    research_db = local_worker_db

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


def test_career_fleet_store_loading(tmp_path):
    """The store round-trips through a real database file.

    The database lives in pytest's tmp_path rather than a NamedTemporaryFile:
    Windows keeps that file open and locked, so sqlite cannot open it there.
    """
    db_path = tmp_path / "career_fleet.db"
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


def test_board_http_server(local_worker_db):
    research_db = local_worker_db

    CareerFleetBoardHandler.database_target = research_db
    CareerFleetBoardHandler.research_database_target = research_db

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
            assert "Career Fleet" in body
            assert "Find a better fit" in body

        # The CRM page ships alongside the board and links back to it.
        with urlopen(f"http://127.0.0.1:{port}/crm.html") as resp:
            assert resp.status == 200
            assert "text/html" in resp.headers.get("Content-Type", "")
            assert "index.html" in resp.read().decode("utf-8")

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

        # Test GET /api/crm against the same research database.
        with urlopen(f"http://127.0.0.1:{port}/api/crm") as resp:
            assert resp.status == 200
            snapshot = json.loads(resp.read().decode("utf-8"))
            assert set(snapshot) >= {"as_of", "summary", "targets", "touches", "interactions"}
            assert isinstance(snapshot["summary"]["targets"], int)

    finally:
        server.shutdown()
        server.server_close()


def test_board_serves_only_its_own_assets():
    """Unknown paths must 404 instead of exposing the process working directory."""
    server = ThreadingHTTPServer(("127.0.0.1", 0), CareerFleetBoardHandler)
    _, port = server.server_address
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        for path in ("/pyproject.toml", "/../pyproject.toml", "/board.py"):
            try:
                with urlopen(f"http://127.0.0.1:{port}{path}") as resp:
                    raise AssertionError(f"{path} unexpectedly served {resp.status}")
            except urllib.error.HTTPError as error:
                assert error.code == 404, (path, error.code)
    finally:
        server.shutdown()
        server.server_close()


def test_board_rejects_a_foreign_host_header():
    """A DNS-rebinding page must not be able to read the databases."""
    server = ThreadingHTTPServer(("127.0.0.1", 0), CareerFleetBoardHandler)
    _, port = server.server_address
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        request = urllib.request.Request(
            f"http://127.0.0.1:{port}/api/jobs", headers={"Host": "evil.example"}
        )
        try:
            urlopen(request)
            raise AssertionError("foreign Host was accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 403
    finally:
        server.shutdown()
        server.server_close()


def test_wave_evaluations_are_not_scored_with_invented_numbers():
    """wave_assessments has no score column, so the board must not fabricate one."""
    research_db = resolve_database_path("career-public-research-worker/career_research.db")
    if not research_db.is_file():
        return
    waves = [e for d in load_job_dossiers(research_db) for e in d["evaluations"] if str(e["id"]).startswith("wave-")]
    assert waves, "expected wave-derived evaluations in the research database"
    assert all(e["score"] is None for e in waves), "wave scores must not be invented"
    assert all(e["verdict"] for e in waves), "the categorical verdict must be surfaced"


def test_board_module_has_no_hardcoded_personal_paths():
    source = (Path(__file__).resolve().parents[1] / "career_fleet" / "board.py").read_text(encoding="utf-8")
    assert "/Users/" not in source, "board.py must not hardcode a personal absolute path"
    assert "Open Design" not in source


def test_database_resolution_is_independent_of_launch_directory(local_worker_db, tmp_path):
    """Database lookup must not depend on cwd: the worker DB is a repo sibling."""
    original = Path.cwd()
    try:
        os.chdir(tmp_path)
        jobs_db = resolve_database_path("career-public-research-worker/career_research.db")
        assert jobs_db.is_file(), f"career_research.db not found at {jobs_db}"
        assert jobs_db == resolve_database_path(), "arg and no-arg resolution must agree"
        crm_db = resolve_research_database_path()
        if not crm_db.is_file():
            pytest.skip("needs the local CRM database too; not present in CI")
    finally:
        os.chdir(original)


def test_embedded_handler_resolves_databases_instead_of_reading_cwd(tmp_path):
    """A handler built without run_board_server() must not fall back to cwd.

    The class-level defaults used to be Path("career_fleet.db"), so an embedded
    server silently answered 503 from any other directory even though the
    resolver could find the real database.
    """
    saved = (CareerFleetBoardHandler.database_target, CareerFleetBoardHandler.research_database_target)
    # A bare relative default is the failure mode this guards: it must stay
    # unset so the resolver, not the process working directory, decides.
    assert saved == (None, None), "handler must not default to a cwd-relative database path"
    CareerFleetBoardHandler.database_target = None
    CareerFleetBoardHandler.research_database_target = None
    original_cwd = Path.cwd()
    server = None
    # A stable directory is all this needs: the point is that resolution ignores
    # cwd. It is deliberately not a TemporaryDirectory, because a handler may
    # still hold a database open and Windows then refuses to delete the tree
    # (WinError 32, and a RecursionError out of the teardown on 3.10).
    try:
        os.chdir(tmp_path)
        try:
            if not resolve_database_path().is_file():
                return  # the research database is not present in this environment

            server = ThreadingHTTPServer(("127.0.0.1", 0), CareerFleetBoardHandler)
            _host, port = server.server_address
            threading.Thread(target=server.serve_forever, daemon=True).start()

            with urlopen(f"http://127.0.0.1:{port}/api/jobs") as resp:
                assert resp.status == 200
                dossiers = json.loads(resp.read().decode("utf-8"))
            assert dossiers, "expected dossiers from the resolved database"

            with urlopen(f"http://127.0.0.1:{port}/api/crm") as resp:
                assert resp.status == 200
        finally:
            if server is not None:
                server.shutdown()
                server.server_close()
    finally:
        os.chdir(original_cwd)
        CareerFleetBoardHandler.database_target = saved[0]
        CareerFleetBoardHandler.research_database_target = saved[1]
