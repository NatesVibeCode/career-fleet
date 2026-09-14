from harness_fleet.sessions import SessionPool, WorkerSession


def test_session_lifecycle():
    pool = SessionPool(num_sessions=3, routes=["r1", "r2"])
    sessions = pool.get_all_sessions()
    assert len(sessions) == 3
    s0 = sessions[0]
    assert s0.status == "active"
    s0.record_batch_success(items_count=5, tokens=200, cost=0.0)
    assert s0.items_completed == 5
    assert s0.tokens_used == 200
    s0.finish()
    assert s0.status == "completed"


def test_error_count_is_exact_while_tail_is_capped():
    session = WorkerSession(session_id="s1", worker_idx=1, route_id="r/a", provider="x")
    for i in range(60):
        session.record_error(f"err {i}")
    assert session.error_count == 60
    assert len(session.errors) == WorkerSession.MAX_RETAINED_ERRORS
    assert session.errors[-1] == "err 59"
    assert session.to_dict()["error_count"] == 60


def test_route_migration_is_recorded():
    session = WorkerSession(session_id="s1", worker_idx=1, route_id="r/a", provider="x")
    assert session.migrate_route("r/a") is False
    assert session.migrate_route("r/b", "y") is True
    assert session.route_id == "r/b"
    assert session.provider == "y"
    assert session.route_history == ["r/a", "r/b"]
