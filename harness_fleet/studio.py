"""Local harness studio: stdlib-only HTTP UI over the fleet control plane.

No third-party web dependencies and no build step: one ``ThreadingHTTPServer``
serving JSON endpoints plus a single static page. Steps are sequenced runs --
each step carries its own task, explicit route picks, budgets, and an
optional paid opt-in with a recorded trust note -- chained by parent run id
so rescoring lineage stays queryable. Paid routes never run implicitly: the
engine admits them only through ``allowed_routes``.
"""
from __future__ import annotations

import json
import shutil
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from .catalog import RouteCatalog
from .engine import Engine
from .input_data import InputItem
from .models import RoutePolicy
from .store import HarnessStore
from .task import PRESETS, create_task_from_preset

STUDIO_DIR = Path(__file__).resolve().parent / "resources" / "studio"


def _send_json(handler: BaseHTTPRequestHandler, status: int, payload: Any) -> None:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def _read_json(handler: BaseHTTPRequestHandler) -> Any:
    length = int(handler.headers.get("Content-Length") or 0)
    if length <= 0:
        return {}
    try:
        return json.loads(handler.rfile.read(length).decode("utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ValueError(f"request body is not JSON: {exc}") from exc


def _store(handler: BaseHTTPRequestHandler) -> HarnessStore:
    return HarnessStore(Path(handler.server.db_path))  # type: ignore[attr-defined]


def _harness_cards() -> list[dict[str, Any]]:
    from .providers.registry import HARNESS_SPECS

    cards = []
    for spec in HARNESS_SPECS:
        path = shutil.which(spec.binary)
        cards.append({
            "name": spec.name,
            "binary": spec.binary,
            "available": path is not None,
            "detail": path or f"{spec.binary} not found in PATH",
        })
    return cards


def _run_step(
    store: HarnessStore,
    workspace: Path,
    step: dict[str, Any],
    parent_run_id: str | None,
) -> dict[str, Any]:
    """Execute one studio step: task + explicit routes + budgets, fail-closed."""
    if not isinstance(step, dict):
        raise ValueError("each step must be an object")
    raw_items = step.get("items") or []
    items = [InputItem.model_validate(item) for item in raw_items]
    if not items:
        raise ValueError("each step needs at least one {item_id, text} item")
    route_ids = [str(route) for route in (step.get("routes") or [])]
    if not route_ids:
        raise ValueError("each step needs at least one route id")
    task_name = str(step.get("task") or "")
    preset = step.get("preset")
    if preset:
        spec = create_task_from_preset(task_name or f"studio-{uuid.uuid4().hex[:8]}", preset_name=str(preset))
        store.register_task(spec)
    elif task_name:
        spec = store.get_task(task_name)
    else:
        raise ValueError("each step needs a task name or a preset")
    allow_paid = bool(step.get("allow_paid", False))
    policy = RoutePolicy(
        allowed_routes=route_ids,
        free_only=not allow_paid,
        note=(str(step.get("paid_note")) if step.get("paid_note") else None),
        max_request_cost=step.get("max_request_cost"),
    )
    run_id = str(step.get("run_id") or f"studio-{uuid.uuid4().hex[:8]}")
    engine = Engine(
        task=spec,
        store=store,
        policy=policy,
        max_attempts_per_batch=int(step.get("max_per_batch", 3)),
    )
    packet = engine.run_campaign(
        raw_items=items,
        run_id=run_id,
        input_path="studio",
        concurrency=int(step.get("concurrency", 2)),
        max_attempts=int(step.get("max_attempts", 20)),
        output_packet_path=workspace / "runs" / run_id / "clean_packet.json",
        parent_run_id=parent_run_id,
    )
    snapshot = store.run_snapshot(run_id)
    audit = packet.get("audit", {})
    return {
        "run_id": run_id,
        "status": snapshot.get("status"),
        "task": spec.name,
        "routes": route_ids,
        "allow_paid": allow_paid,
        "paid_note": policy.note,
        "verified": packet.get("total_verified_records", 0),
        "tokens": audit.get("total_tokens_consumed", 0),
        "cost": audit.get("total_cost_reported", 0.0),
        "receipts": len(packet.get("receipts", [])),
    }


class StudioHandler(BaseHTTPRequestHandler):
    server_version = "HarnessStudio/0.3"

    def log_message(self, *args: Any) -> None:  # keep studio output machine-clean
        pass

    def _workspace(self) -> Path:
        return Path(self.server.workspace_root)  # type: ignore[attr-defined]

    def do_GET(self) -> None:  # noqa: N802
        try:
            path = urlparse(self.path).path
            if path in ("/", "/index.html"):
                page = (STUDIO_DIR / "index.html").read_bytes()
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(page)))
                self.end_headers()
                self.wfile.write(page)
            elif path == "/api/harnesses":
                _send_json(self, 200, {"harnesses": _harness_cards()})
            elif path == "/api/routes":
                catalog = RouteCatalog(db_path=_store(self).path)
                _send_json(self, 200, {"routes": catalog.get_routes(free_only=False, include_disabled=True)})
            elif path == "/api/presets":
                _send_json(self, 200, {"presets": sorted(PRESETS)})
            elif path.startswith("/api/runs/"):
                run_id = path[len("/api/runs/"):]
                _send_json(self, 200, _store(self).run_snapshot(run_id))
            else:
                _send_json(self, 404, {"error": f"unknown path: {path}"})
        except KeyError as exc:
            _send_json(self, 404, {"error": str(exc)})
        except Exception as exc:
            _send_json(self, 500, {"error": str(exc)})

    def do_POST(self) -> None:  # noqa: N802
        try:
            path = urlparse(self.path).path
            if path == "/api/routes/refresh":
                catalog = RouteCatalog(db_path=_store(self).path)
                _send_json(self, 200, {"refresh": catalog.refresh_all()})
            elif path == "/api/runs":
                body = _read_json(self)
                steps = body.get("steps") or []
                if not steps:
                    raise ValueError("body needs a non-empty steps list")
                store = _store(self)
                workspace = self._workspace()
                parent: str | None = None
                results = []
                for step in steps:
                    result = _run_step(store, workspace, step, parent)
                    parent = result["run_id"]
                    results.append(result)
                _send_json(self, 200, {"steps": results})
            else:
                _send_json(self, 404, {"error": f"unknown path: {path}"})
        except (ValueError, KeyError) as exc:
            _send_json(self, 400, {"error": str(exc)})
        except Exception as exc:
            _send_json(self, 500, {"error": str(exc)})


def run_studio_server(
    workspace_root: str | Path = ".",
    db_path: str | Path | None = None,
    port: int = 8080,
) -> None:
    """Serve the studio UI on localhost until interrupted."""
    import os as _os

    workspace = Path(workspace_root).expanduser().resolve()
    if db_path is None:
        configured = _os.environ.get("HARNESS_FLEET_DB")
        resolved = Path(configured).expanduser() if configured else workspace / "harness-fleet.db"
    else:
        resolved = Path(db_path).expanduser()
    server = ThreadingHTTPServer(("127.0.0.1", port), StudioHandler)
    server.workspace_root = str(workspace)  # type: ignore[attr-defined]
    server.db_path = str(resolved)  # type: ignore[attr-defined]
    print(f"harness studio at http://127.0.0.1:{server.server_port} (workspace {workspace})")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
