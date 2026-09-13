from types import SimpleNamespace

import pytest

from career_fleet.cli import cmd_discover
import career_fleet.lanes.lane1_sourcing as lane1
from career_fleet.lanes.lane2_triage import check_dealbreakers, run_lane2_triage
from career_fleet.lanes.lane3_systems import run_lane3_systems
from career_fleet.lanes.lane4_culture import run_lane4_culture
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore


def item(item_id: str, text: str):
    return SimpleNamespace(item_id=item_id, title="Engineer", text=text, source_uri=f"https://example.com/{item_id}")


def test_database_source_text_flows_through_all_lanes(tmp_path):
    store = CareerStore(tmp_path / "career.db")
    store.upsert_company("good", "GoodCo", headcount=10)
    store.add_job_posting(
        "job-good",
        "good",
        "Systems Engineer",
        "Fully remote, engineer-led, async-first team building a distributed storage engine with Kafka and PostgreSQL.",
    )
    profile = IdealEmployerProfile(required_stack=["Kafka", "PostgreSQL"])

    assert run_lane2_triage(store, profile) == {
        "status": "success",
        "total_triaged": 1,
        "survivors": 1,
        "disqualified": 0,
    }
    assert store.list_companies()[0]["status"] == "triaged"
    assert run_lane3_systems(store, profile)["evaluated"] == 1
    assert run_lane4_culture(store, profile)["evaluated"] == 1
    assert store.list_companies()[0]["status"] == "qualified"

    dossier = store.get_company_dossier("good")
    assert dossier["jobs"][0]["raw_text"].startswith("Fully remote")
    assert [evaluation["status"] for evaluation in dossier["evaluations"]] == [
        "triaged",
        "qualified",
        "qualified",
    ]


def test_lane2_uses_source_text_and_metadata_for_hard_filters(tmp_path):
    profile = IdealEmployerProfile(dealbreakers={"policy": "remote_only"})
    store = CareerStore(tmp_path / "career.db")
    store.upsert_company("office", "OfficeCo", headcount=10)
    store.add_job_posting(
        "job-office",
        "office",
        "Engineer",
        "This role is required in the Austin office.",
        location="Austin, TX",
        is_remote=False,
    )

    result = run_lane2_triage(store, profile)

    assert result["disqualified"] == 1
    assert store.list_companies()[0]["status"] == "disqualified"


def test_lane2_honors_configured_location_and_quota_rules():
    profile = IdealEmployerProfile(
        dealbreakers={
            "policy": "any",
            "disallowed_locations": ["Austin"],
            "reject_pure_quota": True,
        }
    )
    assert check_dealbreakers(
        {"id": "x", "headcount": 10},
        [{"raw_text": "Austin location is mandatory"}],
        profile,
    )["rule"] == "configured_location"
    assert check_dealbreakers(
        {"id": "x", "headcount": 10},
        [{"raw_text": "Pure quota cold outbound role"}],
        profile,
    )["rule"] == "pure_quota"


def test_lane2_enforces_timezone_overlap_when_configured():
    profile = IdealEmployerProfile(
        dealbreakers={
            "policy": "any",
            "candidate_timezone": "America/Los_Angeles",
            "min_timezone_overlap_hours": 4.0,
        }
    )
    assert check_dealbreakers(
        {"id": "near", "headcount": 10, "timezone": "America/New_York"},
        [{"raw_text": "Role", "timezone": "America/New_York"}],
        profile,
    ) is None
    assert check_dealbreakers(
        {"id": "far", "headcount": 10, "timezone": "Europe/London"},
        [{"raw_text": "Role", "timezone": "Europe/London"}],
        profile,
    )["rule"] == "timezone_overlap"
    assert check_dealbreakers(
        {"id": "unknown", "headcount": 10},
        [{"raw_text": "Role"}],
        profile,
    )["rule"] == "timezone_overlap_unknown"


def test_recon_only_advances_after_systems_and_is_rerunnable(tmp_path):
    store = CareerStore(tmp_path / "career.db")
    store.upsert_company("good", "GoodCo")
    store.add_job_posting("job-good", "good", "Engineer", "Fully remote engineer-led team.")
    profile = IdealEmployerProfile(required_stack=["Kafka", "PostgreSQL"])

    assert run_lane3_systems(store, profile)["evaluated"] == 0
    assert run_lane2_triage(store, profile)["survivors"] == 1
    assert run_lane3_systems(store, profile)["evaluated"] == 1
    assert run_lane4_culture(store, profile)["evaluated"] == 0
    assert store.list_companies()[0]["status"] == "triaged"

    store.add_job_posting(
        "job-good",
        "good",
        "Engineer",
        "Fully remote, engineer-led, async-first team building a distributed storage engine with Kafka and PostgreSQL.",
    )
    assert run_lane3_systems(store, profile)["evaluated"] == 1
    assert run_lane4_culture(store, profile)["evaluated"] == 1
    assert run_lane2_triage(store, profile)["total_triaged"] == 0
    assert run_lane3_systems(store, profile)["evaluated"] == 0
    assert run_lane4_culture(store, profile)["evaluated"] == 1


@pytest.mark.parametrize(
    ("source", "target", "function_name", "keyword"),
    [
        ("yc", "w24", "fetch_yc_companies", "max_companies"),
        ("greenhouse", "acme", "fetch_greenhouse_board", "max_jobs"),
        ("ashby", "acme", "fetch_ashby_org", "max_jobs"),
        ("lever", "acme", "fetch_lever_org", "max_jobs"),
    ],
)
def test_lane1_uses_current_discovery_signatures(tmp_path, monkeypatch, source, target, function_name, keyword):
    calls = {}

    def fake_fetch(**kwargs):
        calls.update(kwargs)
        return [item("job-1", "Fully remote role")]

    monkeypatch.setattr(lane1, function_name, fake_fetch)
    result = lane1.run_lane1_sourcing(CareerStore(tmp_path / f"{source}.db"), source, target, max_items=3)

    assert result["status"] == "success"
    assert calls[keyword] == 3


def test_lane1_unpacks_site_crawl_result(tmp_path, monkeypatch):
    monkeypatch.setattr(
        lane1,
        "crawl_site",
        lambda **kwargs: ([item("page-1", "Fully remote team")], [{"url": "https://example.com/blocked", "reason": "robots"}]),
    )

    result = lane1.run_lane1_sourcing(CareerStore(tmp_path / "site.db"), "site", "https://example.com", max_items=2)

    assert result == {
        "status": "success",
        "source_type": "site",
        "target": "https://example.com",
        "companies_discovered": 1,
        "postings_added": 1,
        "pages_skipped": 1,
    }


def test_cli_reports_discovery_errors_as_failures(monkeypatch, capsys, tmp_path):
    monkeypatch.setattr(
        "career_fleet.cli.run_lane1_sourcing",
        lambda **kwargs: {"status": "error", "error": "bad source"},
    )

    result = cmd_discover(
        SimpleNamespace(source="greenhouse", target="acme", max=1, db=str(tmp_path / "career.db"))
    )

    assert result == 1
    assert "bad source" in capsys.readouterr().err
