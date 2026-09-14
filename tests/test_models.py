import pytest
from pydantic import ValidationError

from harness_fleet.models import (
    SCHEMA_BASE,
    CleanPacket,
    InputItem,
    ModelOutput,
    ProviderReceipt,
    TaskSpec,
)
from harness_fleet.store import digest_json


def test_model_output_is_closed():
    with pytest.raises(ValidationError):
        ModelOutput.model_validate({
            "items": [{
                "item_id": "i1",
                "source_uri": None,
                "source_digest": "a" * 64,
                "content_type": "text/plain",
                "claims": {},
                "quotes": [{"slice_id": "full", "start": 0, "end": 15, "text": "valid quote text"}],
                "injected": "must not cross boundary",
            }]
        })


def test_task_spec_validates_claims_schema():
    task = TaskSpec(
        name="typed",
        claims_schema={
            "type": "object",
            "properties": {"kind": {"type": "string"}},
            "required": ["kind"],
            "additionalProperties": False,
        },
    )
    task.validate_claims({"kind": "ok"})
    with pytest.raises(ValueError):
        task.validate_claims({"kind": "ok", "extra": True})


def test_receipt_cost_state_must_be_consistent():
    with pytest.raises(ValidationError):
        ProviderReceipt.model_validate({
            "id": "r1",
            "provider": "provider",
            "requested_route": "route",
            "status": "complete",
            "cost": None,
            "cost_status": "reported_zero",
        })


def test_published_models_identify_draft_2020_12_schemas():
    task_schema = TaskSpec.model_json_schema(by_alias=True)
    input_schema = InputItem.model_json_schema(by_alias=True)
    assert task_schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
    assert task_schema["$id"] == f"{SCHEMA_BASE}/task-v1.schema.json"
    assert input_schema["$id"] == f"{SCHEMA_BASE}/input-item-v1.schema.json"


def test_packet_binds_and_revalidates_embedded_task():
    task = TaskSpec(
        name="packet-task",
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )
    payload = {
        "format_version": "harness_fleet_v2",
        "exported_at": "2026-09-09T00:00:00Z",
        "run_id": "run-1",
        "task": task,
        "task_revision": digest_json(task.revision_payload()),
        "input_digest": "b" * 64,
        "total_verified_records": 1,
        "audit": {
            "total_batches_processed": 1,
            "total_tokens_consumed": 0,
            "total_cost_reported": 0,
            "batches_verified": 1,
            "batches_failed": 0,
            "model_attempts": 1,
            "receipts_recorded": 0,
            "unknown_cost_attempts": 0,
        },
        "records": [{
            "item_id": "i1",
            "source_uri": None,
            "source_digest": "c" * 64,
            "content_type": "text/plain",
            "claims": {"summary": "supported", "undeclared": True},
            "quotes": [{"slice_id": "full", "start": 0, "end": 15, "text": "supported quote"}],
        }],
        "receipts": [],
    }
    with pytest.raises(ValidationError, match="Additional properties"):
        CleanPacket.model_validate(payload)
