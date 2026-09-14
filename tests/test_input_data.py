import json

import pytest

from harness_fleet.input_data import InputDataError, load_input_items


def test_input_is_closed_and_canonical(tmp_path):
    path = tmp_path / "input.jsonl"
    path.write_text(json.dumps({"item_id": "i1", "text": "source", "body": "not allowed"}) + "\n")
    with pytest.raises(InputDataError, match="body"):
        load_input_items(path)


def test_invalid_jsonl_is_not_silently_skipped(tmp_path):
    path = tmp_path / "input.jsonl"
    path.write_text('{"item_id":"i1","text":"ok"}\nnot-json\n')
    with pytest.raises(InputDataError, match="line 2"):
        load_input_items(path)


def test_duplicate_ids_are_rejected(tmp_path):
    path = tmp_path / "input.json"
    path.write_text(json.dumps([
        {"item_id": "same", "text": "one"},
        {"item_id": "same", "text": "two"},
    ]))
    with pytest.raises(InputDataError, match="duplicate item_id"):
        load_input_items(path)
