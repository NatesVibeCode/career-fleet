import json

from harness_fleet.input_data import iter_input_items
from harness_fleet.packer import iter_packed_batches


def test_json_array_input_is_lazy(tmp_path):
    path = tmp_path / "items.json"
    path.write_text(json.dumps([
        {"item_id": "first", "text": "first source"},
        {"not_an_input": True},
    ]), encoding="utf-8")

    stream = iter_input_items(path)
    assert next(stream).item_id == "first"


def test_packer_stops_at_one_bounded_batch():
    def records():
        yield {"item_id": "one", "text": "one source"}
        yield {"item_id": "two", "text": "two source"}
        raise AssertionError("the next batch should not be consumed yet")

    batch = next(iter_packed_batches(records(), batch_size=2))
    assert [item["item_id"] for item in batch["items"]] == ["one", "two"]
