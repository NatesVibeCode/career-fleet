import json
from pathlib import Path

from harness_fleet.models import (
    CandidateModelOutput,
    CleanPacket,
    InputItem,
    ModelOutput,
    TaskSpec,
)
from harness_fleet.store import MIGRATION_002_PATH, SCHEMA_PATH, SCHEMA_SQL


def test_published_json_schemas_match_runtime_models():
    root = Path(__file__).resolve().parents[1] / "schemas"
    expected = {
        "task-v1.schema.json": TaskSpec,
        "input-item-v1.schema.json": InputItem,
        "candidate-output-v1.schema.json": CandidateModelOutput,
        "output-v2.schema.json": ModelOutput,
        "packet-v2.schema.json": CleanPacket,
    }
    for name, model in expected.items():
        assert json.loads((root / name).read_text()) == model.model_json_schema(by_alias=True)


def test_database_schema_has_one_packaged_authority():
    assert SCHEMA_PATH.is_file()
    assert SCHEMA_SQL == SCHEMA_PATH.read_text()
    assert MIGRATION_002_PATH.is_file()
    assert "inference_attempts" in MIGRATION_002_PATH.read_text()
