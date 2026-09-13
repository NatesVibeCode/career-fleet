import pytest

import free_fleet
from free_fleet import cli
from free_fleet.models import InputItem, TaskSpec
from free_fleet.profile import IdealCompanyProfile
from free_fleet.store import FreeFleetStore


def _task():
    return TaskSpec(
        name="safety-test",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )


def test_cli_rejects_non_positive_run_limits():
    parser = cli.build_parser()
    with pytest.raises(SystemExit):
        parser.parse_args(["run", "task", "--input", "input.jsonl", "--sessions", "0"])
    with pytest.raises(SystemExit):
        parser.parse_args(["resume", "run", "--sessions", "-1"])


def test_sdk_profile_is_opt_in(tmp_path, monkeypatch):
    db_path = tmp_path / "profile.db"
    store = FreeFleetStore(db_path)
    store.save_profile(IdealCompanyProfile(profile_name="Only when selected"))
    captured = []

    class SpyEngine:
        def __init__(self, *args, **kwargs):
            captured.append(kwargs.get("profile"))

        def run_campaign(self, **kwargs):
            return {"ok": True}

    monkeypatch.setattr(free_fleet, "Engine", SpyEngine)
    item = [InputItem(item_id="one", text="source text")]
    free_fleet.process(_task(), item, run_id="without-profile", db=db_path)
    assert captured[-1] is None
    free_fleet.process(_task(), item, run_id="with-profile", db=db_path, use_active_profile=True)
    assert captured[-1] is not None


def test_sdk_accepts_one_shot_iterables_without_materializing(tmp_path, monkeypatch):
    captured = {}

    class SpyEngine:
        def __init__(self, *args, **kwargs):
            pass

        def run_campaign(self, **kwargs):
            captured["raw_items"] = kwargs["raw_items"]
            return {"ok": True}

    monkeypatch.setattr(free_fleet, "Engine", SpyEngine)
    source = (item for item in [{"item_id": "one", "text": "source text"}])
    free_fleet.process(_task(), source, run_id="generator-input", db=tmp_path / "generator.db")
    assert captured["raw_items"] is source
