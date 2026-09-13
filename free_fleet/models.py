"""Closed data contracts used at trust boundaries."""
from __future__ import annotations

import hashlib
import json
from copy import deepcopy
from typing import Any, Literal

from jsonschema import Draft202012Validator
from pydantic import BaseModel, ConfigDict, Field, JsonValue, model_validator


SCHEMA_BASE = "https://raw.githubusercontent.com/NatesVibeCode/free-fleet/master/schemas"
ID_PATTERN = r"^[A-Za-z0-9_.-]+$"


class ClosedModel(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, populate_by_name=True)


class InputItem(ClosedModel):
    model_config = ConfigDict(
        json_schema_extra={
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$id": f"{SCHEMA_BASE}/input-item-v1.schema.json",
        }
    )
    schema_uri: Literal[f"{SCHEMA_BASE}/input-item-v1.schema.json"] = Field(
        default=f"{SCHEMA_BASE}/input-item-v1.schema.json",
        alias="$schema",
    )
    item_id: str = Field(description="Stable input identity", min_length=1, max_length=128, pattern=ID_PATTERN)
    text: str = Field(description="Complete source text used for evidence verification", min_length=1)
    title: str | None = Field(default=None, description="Optional source label")
    source_uri: str | None = Field(default=None, description="Optional source locator retained in output")
    content_type: str = Field(
        default="text/plain",
        description="Media type describing text serialization",
        pattern=r"^[a-z0-9.+-]+/[a-z0-9.+-]+$",
    )
    metadata: dict[str, JsonValue] = Field(default_factory=dict, description="Source metadata retained with the item")


class SourceSlice(ClosedModel):
    slice_id: str
    start: int = Field(ge=0)
    end: int = Field(ge=0)
    text: str
    partial: bool

    @model_validator(mode="after")
    def exact_length(self) -> "SourceSlice":
        if self.end - self.start != len(self.text):
            raise ValueError("slice offsets must match text length")
        return self


class PackedItem(ClosedModel):
    item_id: str
    title: str | None = None
    source_uri: str | None = None
    content_type: str
    metadata: dict[str, JsonValue]
    source_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    slices: list[SourceSlice] = Field(min_length=1)
    full_char_length: int = Field(ge=1)


class PackedBatch(ClosedModel):
    batch_id: str = Field(pattern=r"^batch_[0-9a-f]{16}$")
    items: list[PackedItem] = Field(min_length=1)


class QuoteRef(ClosedModel):
    slice_id: str = Field(description="Exact source slice containing the quote", min_length=1)
    start: int = Field(description="Inclusive absolute character offset", ge=0)
    end: int = Field(description="Exclusive absolute character offset", gt=0)
    text: str = Field(description="Exact source substring at start:end", min_length=1)

    @model_validator(mode="after")
    def valid_range(self) -> "QuoteRef":
        if self.end <= self.start:
            raise ValueError("quote end must be greater than start")
        return self


class QuoteCandidate(ClosedModel):
    slice_id: str = Field(min_length=1)
    text: str = Field(min_length=1)
    start: int | None = Field(default=None, ge=0)
    end: int | None = Field(default=None, gt=0)

    @model_validator(mode="after")
    def complete_optional_range(self) -> "QuoteCandidate":
        if (self.start is None) != (self.end is None):
            raise ValueError("quote start and end must be supplied together")
        if self.start is not None and self.end is not None and self.end <= self.start:
            raise ValueError("quote end must be greater than start")
        return self


class ExtractedItem(ClosedModel):
    item_id: str = Field(min_length=1)
    source_uri: str | None = None
    source_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    content_type: str
    claims: dict[str, JsonValue]
    quotes: list[QuoteRef] = Field(min_length=1)


class ModelOutput(ClosedModel):
    model_config = ConfigDict(
        json_schema_extra={
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$id": f"{SCHEMA_BASE}/output-v2.schema.json",
        }
    )
    items: list[ExtractedItem]


class CandidateExtractedItem(ClosedModel):
    item_id: str = Field(min_length=1)
    claims: dict[str, JsonValue]
    quotes: list[QuoteCandidate] = Field(min_length=1)


class CandidateModelOutput(ClosedModel):
    model_config = ConfigDict(
        json_schema_extra={
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$id": f"{SCHEMA_BASE}/candidate-output-v1.schema.json",
        }
    )
    items: list[CandidateExtractedItem]


DEFAULT_CLAIMS_SCHEMA: dict[str, Any] = {
    "type": "object",
    "properties": {
        "summary": {"type": "string"},
        "category": {"type": "string"},
    },
    "required": ["summary", "category"],
    "additionalProperties": False,
}


class TaskSpec(ClosedModel):
    model_config = ConfigDict(
        json_schema_extra={
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$id": f"{SCHEMA_BASE}/task-v1.schema.json",
        }
    )
    schema_uri: Literal[f"{SCHEMA_BASE}/task-v1.schema.json"] = Field(
        default=f"{SCHEMA_BASE}/task-v1.schema.json",
        alias="$schema",
    )
    format_version: Literal["free_fleet_task_v1", "bulk_lanes_task_v1"] = "free_fleet_task_v1"
    name: str = Field(description="Stable task name", min_length=1, max_length=128, pattern=ID_PATTERN)
    instructions: str = Field(
        default="Extract only facts supported by the supplied source slices.",
        description="Outcome-specific directions; field structure belongs in claims_schema",
    )
    batch_size: int = Field(default=6, description="Maximum input items per model request", ge=1, le=100)
    max_slice_chars: int = Field(default=6000, description="Maximum source characters exposed per item", ge=300, le=100_000)
    min_quote_chars: int = Field(default=15, description="Minimum admitted evidence-quote length", ge=1, le=10_000)
    claims_schema: dict[str, Any] = Field(
        default_factory=lambda: deepcopy(DEFAULT_CLAIMS_SCHEMA),
        description="Draft 2020-12 object schema; type=object and additionalProperties=false are required",
    )

    @model_validator(mode="after")
    def closed_claims_schema(self) -> "TaskSpec":
        Draft202012Validator.check_schema(self.claims_schema)
        if self.claims_schema.get("type") != "object":
            raise ValueError("claims_schema must describe an object")
        if self.claims_schema.get("additionalProperties") is not False:
            raise ValueError("claims_schema must set additionalProperties to false")
        return self

    def validate_claims(self, claims: dict[str, JsonValue]) -> None:
        errors = sorted(
            Draft202012Validator(self.claims_schema).iter_errors(claims),
            key=lambda error: tuple(str(part) for part in error.absolute_path),
        )
        if errors:
            raise ValueError(errors[0].message)

    def render_prompt(self, items: list[dict[str, Any]]) -> str:
        import json

        contract = {
            "type": "object",
            "additionalProperties": False,
            "required": ["items"],
            "properties": {
                "items": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "additionalProperties": False,
                        "required": ["item_id", "claims", "quotes"],
                        "properties": {
                            "item_id": {"type": "string"},
                            "claims": self.claims_schema,
                            "quotes": {
                                "type": "array",
                                "minItems": 1,
                                "items": {
                                    "type": "object",
                                    "additionalProperties": False,
                                    "required": ["slice_id", "text"],
                                    "properties": {
                                        "slice_id": {"type": "string"},
                                        "start": {"type": "integer", "minimum": 0},
                                        "end": {"type": "integer", "minimum": 1},
                                        "text": {"type": "string"},
                                    },
                                },
                            },
                        },
                    },
                },
            },
        }
        payload = {"output_schema": contract, "input_items": items}
        return f"{self.instructions}\nReturn JSON only. Copy exact quote text from one named slice; offsets are optional.\n{json.dumps(payload, ensure_ascii=False)}"


class RoutePolicy(ClosedModel):
    allowed_transports: list[str] | None = Field(default=None, description="Optional allowlist of free-fleet transport names")
    excluded_transports: list[str] = Field(default_factory=list, description="Blocklist of free-fleet transport names")
    allowed_providers: list[str] | None = Field(default=None, description="Deprecated alias for allowed_transports")
    excluded_providers: list[str] = Field(default_factory=list, description="Deprecated alias for excluded_transports")
    allowed_routes: list[str] | None = Field(default=None, description="Optional allowlist of route IDs")
    excluded_routes: list[str] = Field(default_factory=list, description="Blocklist of route IDs")
    zdr: bool = Field(default=False, description="Enforce Zero Data Retention on upstream providers")
    allow_data_collection: bool = Field(default=True, description="Whether providers may collect request data")
    max_cost_per_1k_input: float = Field(default=0.0, ge=0, description="Max allowed cost per 1k input tokens")
    max_cost_per_1k_output: float = Field(default=0.0, ge=0, description="Max allowed cost per 1k output tokens")
    max_request_cost: float | None = Field(default=None, ge=0, description="Max allowed spend per single request")
    free_only: bool = Field(default=False, description="Explicit flag to restrict to observed-zero routes only")
    openrouter_providers: list[str] | None = Field(default=None, description="Upstream OpenRouter providers to prioritize")
    openrouter_ignore: list[str] = Field(default_factory=list, description="Upstream OpenRouter providers to ignore")
    openrouter_order: list[str] | None = Field(default=None, description="Upstream OpenRouter provider ordering")
    openrouter_allow_fallbacks: bool = Field(default=True, description="Whether OpenRouter may fall back to other providers")

    @model_validator(mode="after")
    def sync_transports_and_providers(self) -> "RoutePolicy":
        if self.allowed_providers and not self.allowed_transports:
            self.allowed_transports = list(self.allowed_providers)
        elif self.allowed_transports and not self.allowed_providers:
            self.allowed_providers = list(self.allowed_transports)
        if self.excluded_providers and not self.excluded_transports:
            self.excluded_transports = list(self.excluded_providers)
        elif self.excluded_transports and not self.excluded_providers:
            self.excluded_providers = list(self.excluded_transports)
        return self


class ProviderReceipt(ClosedModel):
    id: str
    session_id: str | None = None
    provider: str
    requested_route: str
    status: Literal["complete", "failed"]
    cost: float | None = Field(default=None, ge=0)
    cost_status: Literal["reported_zero", "billed", "unknown"] = "unknown"
    usage: dict[str, JsonValue] | None = None
    error: str | None = None
    duration_seconds: float | None = Field(default=None, ge=0)
    error_type: Literal["rate_limit", "transient_http", "inference_error", "auth_error", "timeout"] | None = None
    retry_after: float | None = Field(default=None, ge=0)

    @model_validator(mode="after")
    def consistent_cost(self) -> "ProviderReceipt":
        expected = "unknown" if self.cost is None else ("reported_zero" if self.cost == 0 else "billed")
        if self.cost_status != expected:
            raise ValueError(f"cost_status must be '{expected}' for cost={self.cost}")
        return self


class PacketAudit(ClosedModel):
    total_batches_processed: int = Field(ge=0)
    total_tokens_consumed: int = Field(ge=0)
    total_cost_reported: float = Field(ge=0)
    batches_verified: int = Field(ge=0)
    batches_failed: int = Field(ge=0)
    model_attempts: int = Field(ge=0)
    receipts_recorded: int = Field(ge=0)
    unknown_cost_attempts: int = Field(ge=0)


class CleanPacket(ClosedModel):
    model_config = ConfigDict(
        json_schema_extra={
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$id": f"{SCHEMA_BASE}/packet-v2.schema.json",
        }
    )
    schema_uri: Literal[f"{SCHEMA_BASE}/packet-v2.schema.json"] = Field(
        default=f"{SCHEMA_BASE}/packet-v2.schema.json",
        alias="$schema",
    )
    format_version: Literal["free_fleet_v2", "bulk_lanes_v2"] = "free_fleet_v2"
    exported_at: str
    run_id: str = Field(min_length=1, max_length=128, pattern=ID_PATTERN)
    task: TaskSpec
    task_revision: str = Field(pattern=r"^[0-9a-f]{64}$")
    input_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    total_verified_records: int = Field(ge=0)
    audit: PacketAudit
    records: list[ExtractedItem]
    receipts: list[ProviderReceipt]
    policy: RoutePolicy | None = None

    @model_validator(mode="after")
    def consistent_record_count(self) -> "CleanPacket":
        if self.total_verified_records != len(self.records):
            raise ValueError("total_verified_records does not match records")
        task_payload = self.task.model_dump(mode="json", by_alias=True)
        task_digest = hashlib.sha256(
            json.dumps(task_payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        ).hexdigest()
        if self.task_revision != task_digest:
            raise ValueError("task_revision does not match the embedded task")
        item_ids = [record.item_id for record in self.records]
        if len(item_ids) != len(set(item_ids)):
            raise ValueError("records contain duplicate item_id values")
        for record in self.records:
            self.task.validate_claims(record.claims)
        return self


class RouteInfo(ClosedModel):
    id: str
    provider: str | None = None
    enabled: bool
    price_state: Literal["candidate", "price_observed_zero", "unknown", "disabled"]
    auth: str | None = None
    cost_per_1k_input: float | None = Field(default=None, ge=0)
    cost_per_1k_output: float | None = Field(default=None, ge=0)
    last_verified: str | None = None
    verification_source: str | None = None
    last_price_observation: float | None = None
    disabled_reason: str | None = None
    disabled_at: float | None = None


class RoutesResult(ClosedModel):
    routes: list[RouteInfo]
    refresh: dict[str, int | str] | None = None


class WorkerSessionRecord(ClosedModel):
    session_id: str
    worker_idx: int = Field(ge=1)
    route_id: str
    provider: str
    status: Literal["active", "completed", "failed"]
    started_at: str
    last_active: str
    batches_completed: int = Field(ge=0)
    items_completed: int = Field(ge=0)
    tokens_used: int = Field(ge=0)
    reported_cost: float = Field(ge=0)
    errors: list[str]


class BatchTestResult(ClosedModel):
    ok: bool
    results: list[ExtractedItem] | None = None
    receipt: ProviderReceipt | None = None
    error: str | None = None


class ValidationReport(ClosedModel):
    valid: bool
    task: str
    input_items: int
    batches: int
    errors: list[str] = Field(default_factory=list)


class TaskRegistrationResult(ClosedModel):
    task: str
    revision: str = Field(pattern=r"^[0-9a-f]{64}$")


class ProfileResult(ClosedModel):
    profile_kind: Literal["ideal_company", "ideal_employer"]
    revision: str = Field(pattern=r"^[0-9a-f]{64}$")
    profile: dict[str, JsonValue]


class TaskSummary(ClosedModel):
    task_name: str
    revision_id: str = Field(pattern=r"^[0-9a-f]{64}$")
    created_at: str


class TasksResult(ClosedModel):
    tasks: list[TaskSummary]
    count: int = Field(ge=0)

    @model_validator(mode="after")
    def consistent_count(self) -> "TasksResult":
        if self.count != len(self.tasks):
            raise ValueError("task count does not match tasks")
        return self


class DoctorCheck(ClosedModel):
    name: str
    ok: bool
    detail: str


class DoctorReport(ClosedModel):
    ready: bool
    database: str
    checks: list[DoctorCheck]


class SchemaResult(ClosedModel):
    kind: Literal["task", "input", "candidate-output", "output", "packet", "profile", "database"]
    schema_document: dict[str, JsonValue]


class SetupAction(ClosedModel):
    kind: Literal["skill", "database", "routes"]
    status: Literal["planned", "created", "updated", "unchanged", "skipped"]
    path: str | None = None
    detail: str | None = None


class StdioServerConfig(ClosedModel):
    command: str
    args: list[str]


class SetupReport(ClosedModel):
    ready: bool
    scope: Literal["user", "project"]
    workspace_root: str
    database: str
    skill_path: str
    actions: list[SetupAction]
    stdio_server: StdioServerConfig
    route_refresh: dict[str, int | str] | None = None
    next_commands: list[list[str]]


class BatchStatusCounts(ClosedModel):
    total: int = Field(default=0, ge=0)
    verified: int = Field(default=0, ge=0)
    pending: int = Field(default=0, ge=0)
    leased: int = Field(default=0, ge=0)
    failed: int = Field(default=0, ge=0)


class RouteStatusSummary(ClosedModel):
    route_id: str
    provider: str
    attempts: int = Field(default=0, ge=0)
    verified: int = Field(default=0, ge=0)
    rate_limits: int = Field(default=0, ge=0)
    success_rate: float = Field(default=0.0, ge=0.0, le=1.0)
    avg_latency_seconds: float = Field(default=0.0, ge=0.0)
    reported_cost: float = Field(default=0.0, ge=0.0)


class RunStatusReport(ClosedModel):
    run_id: str
    status: str
    task_name: str
    profile_revision_id: str | None = None
    total_items: int = Field(default=0, ge=0)
    verified_items: int = Field(default=0, ge=0)
    batches: BatchStatusCounts
    attempts_used: int = Field(default=0, ge=0)
    max_attempts: int = Field(default=0, ge=0)
    rate_limits_encountered: int = Field(default=0, ge=0)
    routes: list[RouteStatusSummary] = Field(default_factory=list)
    active_workers: int = Field(default=0, ge=0)
    recent_errors: list[str] = Field(default_factory=list)


class RouteEvalResult(ClosedModel):
    route_id: str
    provider: str
    total_samples: int = Field(ge=0)
    schema_pass_count: int = Field(ge=0)
    grounding_pass_count: int = Field(ge=0)
    correct_count: int | None = Field(default=None, ge=0)
    rate_limit_count: int = Field(default=0, ge=0)
    error_count: int = Field(default=0, ge=0)
    schema_pass_rate: float = Field(ge=0.0, le=1.0)
    grounding_pass_rate: float = Field(ge=0.0, le=1.0)
    accuracy: float | None = Field(default=None, ge=0.0, le=1.0)
    avg_latency_seconds: float = Field(ge=0.0)
    composite_score: float = Field(ge=0.0, le=1.0)


class RouteEvalReport(ClosedModel):
    task: str
    samples: int = Field(ge=0)
    evaluated_at: str
    routes: list[RouteEvalResult] = Field(default_factory=list)


class CooldownDetail(ClosedModel):
    route_id: str
    cooldown_until: str
    reason: str | None = None
    seconds_remaining: float = Field(default=0.0, ge=0.0)


class CooldownsReport(ClosedModel):
    action: str
    count: int = Field(default=0, ge=0)
    cooldowns: list[CooldownDetail] = Field(default_factory=list)
    cleared: int | None = None
    route_id: str | None = None
