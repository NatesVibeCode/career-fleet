"""Free Fleet: Coordinated free & local LLM worker fleet with closed fields and exact source evidence."""
__version__ = "0.2.5"
import json
from pathlib import Path
from .task import load_task_spec
from .engine import Engine
from .catalog import RouteCatalog
from .grounding import verify_grounding
from .models import CleanPacket, ExtractedItem, InputItem, ModelOutput, QuoteRef, TaskSpec
from .input_data import load_input_items
from .discover import fetch_text, run_discovery
from .store import BulkLanesStore, FreeFleetStore
from .slicer import slice_document
from .packer import pack_items
from .export import export_clean_packet

def read_packet(packet_path: str) -> dict:
    """Convenience helper for downstream trusted applications to safely load a clean packet."""
    p = Path(packet_path).resolve()
    if not p.exists():
        raise FileNotFoundError(f"Packet file not found: {packet_path}")
    return CleanPacket.model_validate_json(p.read_text()).model_dump(mode="json", by_alias=True)

def process(
    task: str | TaskSpec | Path,
    input: str | Path | list[InputItem] | list[dict],
    run_id: str | None = None,
    concurrency: int = 4,
    max_attempts: int = 300,
    output: str | Path | None = None,
    policy=None,
    db: str | Path | None = None,
    **load_kwargs,
) -> dict:
    """High-level Python SDK: `free_fleet.process(task, input, ...) -> packet`.

    Example:
        import free_fleet
        packet = free_fleet.process("my-task", "data.csv", concurrency=8)
        df = packet["records"]  # or use export helpers

    Supports local paths (.csv/.jsonl/.json/.txt/.html/.pdf), lists of InputItem,
    or pandas DataFrames (if installed) via `input=df`.
    """
    import time as _time

    # Resolve task
    from pathlib import Path as _P

    store = FreeFleetStore(_P(db)) if db is not None else FreeFleetStore()
    if isinstance(task, TaskSpec):
        spec = task
        store.register_task(spec)
    elif isinstance(task, (str, _P)) and _P(str(task)).is_file():
        spec = load_task_spec(str(task))
        store.register_task(spec)
    else:
        spec = store.get_task(str(task))

    # Resolve input items (DataFrame support optional)
    items: list[InputItem]
    input_path_str = str(input) if isinstance(input, (str, _P)) else "python-sdk"
    if isinstance(input, list):
        # Already InputItem/dict list
        items = [i if isinstance(i, InputItem) else InputItem.model_validate(i) for i in input]  # type: ignore
    elif isinstance(input, (str, _P)) and _P(str(input)).is_file():
        items = load_input_items(str(input), **load_kwargs)  # type: ignore
    else:
        # Try DataFrame-like (has to_dict)
        try:
            if hasattr(input, "to_dict") and hasattr(input, "columns"):
                # pandas DataFrame
                recs = input.to_dict(orient="records")  # type: ignore
                # Heuristic columns: try to detect id/text columns
                items = []
                for idx, rec in enumerate(recs):
                    # Prefer explicit id column
                    cand_id = rec.get("item_id") or rec.get("id") or f"row_{idx}"
                    txt = rec.get("text") or rec.get("body") or rec.get("content") or rec.get("description") or ""
                    if not txt:
                        # fallback: first stringifiable column value
                        for v in rec.values():
                            if isinstance(v, str) and len(v.strip()) > 20:
                                txt = v
                                break
                    items.append(InputItem(item_id=str(cand_id), text=str(txt), metadata={k: v for k, v in rec.items() if k not in ("item_id", "id", "text", "body", "content")}))
            else:
                raise TypeError("unsupported input type")
        except Exception as e:
            raise ValueError(f"Unable to resolve input '{input}': {e}") from e

    rid = run_id or f"sdk-{int(_time.time())}"
    out_path = _P(output) if output is not None else _P(f"runs/{rid}/clean_packet.json")
    eng = Engine(task=spec, store=store, policy=policy)
    return eng.run_campaign(raw_items=items, run_id=rid, input_path=input_path_str, concurrency=concurrency, max_attempts=max_attempts, output_packet_path=out_path)

__all__ = [
    "load_task_spec",
    "Engine",
    "RouteCatalog",
    "verify_grounding",
    "CleanPacket",
    "ExtractedItem",
    "InputItem",
    "ModelOutput",
    "QuoteRef",
    "TaskSpec",
    "FreeFleetStore",
    "BulkLanesStore",
    "load_input_items",
    "fetch_text",
    "run_discovery",
    "slice_document",
    "pack_items",
    "export_clean_packet",
    "read_packet",
    "process",
    "__version__",
]
