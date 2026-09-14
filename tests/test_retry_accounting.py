from harness_fleet.models import TaskSpec
from harness_fleet.packer import iter_packed_batches
from harness_fleet.store import HarnessStore


def _task():
    return TaskSpec(
        name="retry-accounting-test",
        batch_size=1,
        claims_schema={
            "type": "object",
            "properties": {"summary": {"type": "string"}},
            "required": ["summary"],
            "additionalProperties": False,
        },
    )


def test_rate_limits_leave_counting_attempts_available(tmp_path):
    store = HarnessStore(tmp_path / "retry.db")
    revision = store.register_task(_task())
    store.create_run(
        "retry-run", revision, "input", "a" * 64, 1, 20, 1, "output"
    )
    batch = next(iter_packed_batches([{"item_id": "one", "text": "source one"}], batch_size=1))
    store.enqueue_batches("retry-run", [batch], max_attempts_per_batch=2)

    for number in range(3):
        lease = store.lease_batch("retry-run", f"rate-worker-{number}")
        assert lease is not None
        store.release_lease("retry-run", lease["attempt_id"], f"rate-worker-{number}", "429 rate limit")

    first_failure = store.lease_batch("retry-run", "failure-worker")
    assert first_failure is not None
    store.fail_batch("retry-run", first_failure["attempt_id"], "failure-worker", "permanent failure", None)
    assert next(iter(store.run_snapshot("retry-run")["batches"].values()))["status"] == "pending"

    second_failure = store.lease_batch("retry-run", "failure-worker")
    assert second_failure is not None
    store.fail_batch("retry-run", second_failure["attempt_id"], "failure-worker", "permanent failure", None)
    assert next(iter(store.run_snapshot("retry-run")["batches"].values()))["status"] == "failed"
