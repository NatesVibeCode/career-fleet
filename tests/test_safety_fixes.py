import json

import pytest

import harness_fleet
from harness_fleet import cli
from harness_fleet.models import InputItem, TaskSpec
from harness_fleet.profile import IdealCompanyProfile
from harness_fleet.providers.demo import DemoProvider
from harness_fleet.store import HarnessStore


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
    store = HarnessStore(db_path)
    store.save_profile(IdealCompanyProfile(profile_name="Only when selected"))
    captured = []

    class SpyEngine:
        def __init__(self, *args, **kwargs):
            captured.append(kwargs.get("profile"))

        def run_campaign(self, **kwargs):
            return {"ok": True}

    monkeypatch.setattr(harness_fleet, "Engine", SpyEngine)
    item = [InputItem(item_id="one", text="source text")]
    harness_fleet.process(_task(), item, run_id="without-profile", db=db_path)
    assert captured[-1] is None
    harness_fleet.process(_task(), item, run_id="with-profile", db=db_path, use_active_profile=True)
    assert captured[-1] is not None


def test_sdk_accepts_one_shot_iterables_without_materializing(tmp_path, monkeypatch):
    captured = {}

    class SpyEngine:
        def __init__(self, *args, **kwargs):
            pass

        def run_campaign(self, **kwargs):
            captured["raw_items"] = kwargs["raw_items"]
            return {"ok": True}

    monkeypatch.setattr(harness_fleet, "Engine", SpyEngine)
    source = (item for item in [{"item_id": "one", "text": "source text"}])
    harness_fleet.process(_task(), source, run_id="generator-input", db=tmp_path / "generator.db")
    assert captured["raw_items"] is source


def test_demo_provider_ignores_profile_json_before_task_payload():
    profile = IdealCompanyProfile(
        profile_name="Profile with nested JSON",
        calibrated_scoring_rubric={"tier_1": "high fit"},
    )
    task = _task()
    prompt = f"{profile.to_prompt_context()}\n\n{task.render_prompt([{'item_id': 'one', 'sections': [{'slice_id': 'full', 'text': 'A sufficiently long source quote.'}]}])}"
    ok, response, receipt = DemoProvider().run_prompt("demo/fake", prompt)
    assert ok is True
    assert receipt.status == "complete"
    assert response is not None and '"items"' in response


def test_demo_provider_disambiguates_repeated_quote_with_offsets():
    source = "Introducing Browserbase Agents: One Prompt, One API Call.\n" * 2
    prompt = (
        'instructions\n{"input_items": [{"item_id": "one", "sections": '
        '[{"slice_id": "full", "start": 0, "end": ' + str(len(source)) + ', '
        '"text": ' + json.dumps(source) + '}], "title": "demo"}], '
        '"output_schema": {"properties": {"items": {"items": '
        '{"properties": {"claims": {"type": "object", "properties": '
        '{"summary": {"type": "string"}}}}}}}}}'
    )
    ok, response, receipt = DemoProvider().run_prompt("demo/fake", prompt)
    assert ok is True
    assert receipt.status == "complete"
    quote = json.loads(response)["items"][0]["quotes"][0]
    assert quote["start"] == 0
    assert quote["end"] == len(quote["text"])
