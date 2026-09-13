"""Multi-session coordinator for parallel model worker lanes."""
import json
import time
import uuid
from pathlib import Path
from typing import Dict, List, Optional, Any

class WorkerSession:
    """Represents a dedicated worker session executing across a free model lane."""
    def __init__(self, session_id: str, worker_idx: int, route_id: str, provider: str):
        self.session_id = session_id
        self.worker_idx = worker_idx
        self.route_id = route_id
        self.provider = provider
        self.status = "active"
        self.started_at = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        self.last_active = self.started_at
        self.batches_completed = 0
        self.items_completed = 0
        self.tokens_used = 0
        self.reported_cost = 0.0
        self.errors: List[str] = []

    def record_batch_success(self, items_count: int, tokens: int, cost: float):
        self.batches_completed += 1
        self.items_completed += items_count
        self.tokens_used += tokens
        self.reported_cost += cost
        self.last_active = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    def record_error(self, err: str):
        self.errors.append(err)
        self.last_active = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    def finish(self, status: str = "completed"):
        self.status = status
        self.last_active = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    def to_dict(self) -> dict:
        return {
            "session_id": self.session_id,
            "worker_idx": self.worker_idx,
            "route_id": self.route_id,
            "provider": self.provider,
            "status": self.status,
            "started_at": self.started_at,
            "last_active": self.last_active,
            "batches_completed": self.batches_completed,
            "items_completed": self.items_completed,
            "tokens_used": self.tokens_used,
            "reported_cost": self.reported_cost,
            "errors": self.errors[-3:] # keep last 3 errors
        }

class SessionPool:
    """Manages a fleet of concurrent worker sessions."""
    def __init__(self, num_sessions: int, routes: List[str]):
        if not routes:
            raise ValueError("SessionPool requires at least one route")
        self.num_sessions = num_sessions
        self.routes = routes
        self.sessions: Dict[str, WorkerSession] = {}
        self._init_sessions()

    def _init_sessions(self):
        for i in range(self.num_sessions):
            sid = f"sess_{i+1:02d}_{uuid.uuid4().hex[:8]}"
            route_id = self.routes[i % len(self.routes)]
            provider = route_id.split("/", 1)[0] if "/" in route_id else "unknown"
            self.sessions[sid] = WorkerSession(
                session_id=sid,
                worker_idx=i + 1,
                route_id=route_id,
                provider=provider
            )

    def get_session(self, session_id: str) -> Optional[WorkerSession]:
        return self.sessions.get(session_id)

    def get_all_sessions(self) -> List[WorkerSession]:
        return list(self.sessions.values())

    def to_dict(self) -> dict:
        return {sid: sess.to_dict() for sid, sess in self.sessions.items()}
