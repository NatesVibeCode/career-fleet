"""Air-gap boundary packet and CSV exporter."""
import csv
import json
import time
from pathlib import Path
from typing import Any
from .models import CleanPacket, ExtractedItem, ProviderReceipt, RoutePolicy, TaskSpec


def _evaluate_filter(claims: dict[str, Any], filter_expr: str) -> bool:
    expr = filter_expr.strip()
    op = None
    for candidate in (">=", "<=", "!=", "==", "=", ">", "<"):
        if candidate in expr:
            op = candidate
            break
    if not op:
        raise ValueError(f"invalid filter expression '{filter_expr}'; must contain ==, =, !=, >=, <=, >, or <")

    key, val_str = [p.strip() for p in expr.split(op, 1)]
    val_str = val_str.strip("'\"")
    if key not in claims:
        return False
    actual = claims[key]

    if val_str.lower() in ("true", "false"):
        expected: Any = (val_str.lower() == "true")
    elif val_str.lower() in ("null", "none"):
        expected = None
    else:
        try:
            if "." in val_str:
                expected = float(val_str)
            else:
                expected = int(val_str)
        except ValueError:
            expected = val_str

    try:
        if op in ("=", "=="):
            if expected is None:
                return actual is None
            if actual is None:
                return False
            return actual == expected or str(actual).lower() == str(expected).lower()
        elif op == "!=":
            if expected is None:
                return actual is not None
            if actual is None:
                return True
            return actual != expected and str(actual).lower() != str(expected).lower()
        elif op == ">=":
            return float(actual) >= float(expected)
        elif op == "<=":
            return float(actual) <= float(expected)
        elif op == ">":
            return float(actual) > float(expected)
        elif op == "<":
            return float(actual) < float(expected)
    except (ValueError, TypeError):
        return False
    return False


def adjust_claim_score(raw: Any, bias: float = 0.0, low: float = 0.0, high: float = 100.0) -> float | None:
    """Return bias-corrected numeric claim score, clamped to [low, high]. None if non-numeric."""
    try:
        return max(low, min(high, float(raw) - float(bias)))
    except (ValueError, TypeError):
        return None


def _build_item_route_map(run_data: dict) -> dict[str, str]:
    """Map item_id -> producing route_id via batch receipts. Best-effort; missing entries omitted."""
    mapping: dict[str, str] = {}
    for batch in (run_data.get("batches") or {}).values():
        receipt = batch.get("receipt") or {}
        route_id = receipt.get("requested_route") or receipt.get("route_id")
        if not route_id:
            continue
        result = batch.get("result")
        items: list[dict] = []
        if isinstance(result, list):
            items = result
        elif isinstance(result, dict) and isinstance(result.get("items"), list):
            items = result["items"]
        for item in items:
            item_id = item.get("item_id") if isinstance(item, dict) else getattr(item, "item_id", None)
            if item_id:
                mapping[str(item_id)] = str(route_id)
    return mapping


def _filter_and_sort_records(
    records: list[ExtractedItem],
    sort_by: str | None = None,
    descending: bool = True,
    top: int | None = None,
    filter_expr: str | None = None,
    bias_map: dict[str, float] | None = None,
    score_field: str | None = None,
    item_route_map: dict[str, str] | None = None,
) -> list[ExtractedItem]:
    """Filter/sort records. When bias_map+score_field are given and sort_by==score_field,
    ranking uses bias-adjusted scores (raw - route bias) so mixed-rater CSVs stay comparable.
    Raw claims are never mutated; only the sort key changes."""
    res = list(records)
    if filter_expr:
        res = [r for r in res if _evaluate_filter(r.claims, filter_expr)]
    if sort_by:
        def sort_key(rec: ExtractedItem):
            v = rec.claims.get(sort_by)
            if v is None:
                return (0, 0.0, "")
            try:
                numeric = float(v)
            except (ValueError, TypeError):
                return (1 if descending else 2, 0.0, str(v))
            if bias_map and score_field and sort_by == score_field and item_route_map:
                route_id = item_route_map.get(rec.item_id)
                if route_id and route_id in bias_map:
                    adj = adjust_claim_score(numeric, bias_map[route_id])
                    if adj is not None:
                        numeric = adj
            return (2 if descending else 1, numeric, "")
        res.sort(key=sort_key, reverse=descending)
    if top is not None:
        if top < 0:
            raise ValueError("top must be non-negative")
        res = res[:top]
    return res


def export_clean_csv(
    run_data: dict,
    output_path: Path,
    sort_by: str | None = None,
    descending: bool = True,
    top: int | None = None,
    rank: bool = False,
    filter_expr: str | None = None,
    bias_map: dict[str, float] | None = None,
    score_field: str | None = None,
) -> Path:
    """Project verified records into a frictionless tabular CSV format."""
    output_path.parent.mkdir(parents=True, exist_ok=True)
    
    verified_records: list[ExtractedItem] = []
    task = TaskSpec.model_validate(run_data["task"])

    for b in run_data.get("batches", {}).values():
        if b.get("status") == "verified" and b.get("result"):
            results = b["result"]
            if isinstance(results, list):
                validated = [ExtractedItem.model_validate(item) for item in results]
            elif isinstance(results, dict) and "items" in results:
                validated = [ExtractedItem.model_validate(item) for item in results["items"]]
            else:
                raise ValueError("verified batch result has an invalid shape")
            for item in validated:
                task.validate_claims(item.claims)
            verified_records.extend(validated)

    item_route_map = _build_item_route_map(run_data) if bias_map and score_field else None
    verified_records = _filter_and_sort_records(
        verified_records,
        sort_by=sort_by,
        descending=descending,
        top=top,
        filter_expr=filter_expr,
        bias_map=bias_map,
        score_field=score_field,
        item_route_map=item_route_map,
    )

    # Determine all unique claim keys, excluding reserved standard column headers
    reserved_headers = {"item_id", "primary_quote_text", "quote_count", "source_uri", "source_digest"}
    if rank:
        reserved_headers.add("rank")
    claim_keys: list[str] = []
    if task.claims_schema and "properties" in task.claims_schema:
        claim_keys = [k for k in task.claims_schema["properties"].keys() if k not in reserved_headers]
    for rec in verified_records:
        for k in rec.claims.keys():
            if k not in claim_keys and k not in reserved_headers:
                claim_keys.append(k)

    fieldnames = []
    if rank:
        fieldnames.append("rank")
    fieldnames.append("item_id")
    fieldnames.extend(claim_keys)
    fieldnames.extend(["primary_quote_text", "quote_count", "source_uri", "source_digest"])

    with open(output_path, mode="w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        for idx, rec in enumerate(verified_records, start=1):
            row = {
                "item_id": rec.item_id,
                "primary_quote_text": rec.quotes[0].text if rec.quotes else "",
                "quote_count": len(rec.quotes),
                "source_uri": rec.source_uri or "",
                "source_digest": rec.source_digest,
            }
            if rank:
                row["rank"] = idx
            for k in claim_keys:
                val = rec.claims.get(k)
                if isinstance(val, (dict, list)):
                    row[k] = json.dumps(val, ensure_ascii=False)
                elif val is not None:
                    row[k] = str(val)
                else:
                    row[k] = ""
            writer.writerow(row)

    return output_path


def export_clean_packet(
    run_data: dict,
    output_path: Path,
    export_format: str = "json",
    sort_by: str | None = None,
    descending: bool = True,
    top: int | None = None,
    rank: bool = False,
    filter_expr: str | None = None,
    bias_map: dict[str, float] | None = None,
    score_field: str | None = None,
) -> dict:
    """Validate and serialize verified records into a closed packet or CSV."""
    output_path.parent.mkdir(parents=True, exist_ok=True)
    
    verified_records: list[ExtractedItem] = []
    receipts: list[ProviderReceipt] = []
    task = TaskSpec.model_validate(run_data["task"])

    for b in run_data.get("batches", {}).values():
        if b.get("status") == "verified" and b.get("result"):
            results = b["result"]
            if isinstance(results, list):
                validated = [ExtractedItem.model_validate(item) for item in results]
            elif isinstance(results, dict) and "items" in results:
                validated = [ExtractedItem.model_validate(item) for item in results["items"]]
            else:
                raise ValueError("verified batch result has an invalid shape")
            for item in validated:
                task.validate_claims(item.claims)
            verified_records.extend(validated)

    item_route_map = _build_item_route_map(run_data) if bias_map and score_field else None
    verified_records = _filter_and_sort_records(
        verified_records,
        sort_by=sort_by,
        descending=descending,
        top=top,
        filter_expr=filter_expr,
        bias_map=bias_map,
        score_field=score_field,
        item_route_map=item_route_map,
    )

    raw_receipts = run_data.get("model_runs")
    if isinstance(raw_receipts, list):
        receipts = [ProviderReceipt.model_validate(receipt) for receipt in raw_receipts]
    else:
        receipts = [
            ProviderReceipt.model_validate(batch["receipt"])
            for batch in run_data.get("batches", {}).values()
            if batch.get("receipt")
        ]
    total_tokens = sum(
        int(receipt.usage.get("total_tokens", 0))
        for receipt in receipts
        if isinstance(receipt.usage, dict)
    )
    total_cost = sum(receipt.cost for receipt in receipts if receipt.cost is not None)

    policy = RoutePolicy.model_validate(run_data["policy"]) if run_data.get("policy") else None

    packet_model = CleanPacket.model_validate({
        "format_version": "free_fleet_v2",
        "exported_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "run_id": run_data.get("run_id"),
        "task": task,
        "task_revision": run_data["task_revision"],
        "input_digest": run_data["input_digest"],
        "total_verified_records": len(verified_records),
        "audit": {
            "total_batches_processed": len(run_data.get("batches", {})),
            "total_tokens_consumed": total_tokens,
            "total_cost_reported": total_cost,
            "batches_verified": sum(1 for b in run_data.get("batches", {}).values() if b.get("status") == "verified"),
            "batches_failed": sum(1 for b in run_data.get("batches", {}).values() if b.get("status") == "failed"),
            "model_attempts": int(run_data.get("attempts_used", len(receipts))),
            "receipts_recorded": len(receipts),
            "unknown_cost_attempts": sum(1 for receipt in receipts if receipt.cost is None),
        },
        "records": verified_records,
        "receipts": receipts,
        "policy": policy,
    })
    packet = packet_model.model_dump(mode="json", by_alias=True)

    if export_format == "csv" or output_path.suffix.lower() == ".csv":
        export_clean_csv(
            run_data,
            output_path,
            sort_by=sort_by,
            descending=descending,
            top=top,
            rank=rank,
            filter_expr=filter_expr,
            bias_map=bias_map,
            score_field=score_field,
        )
    elif export_format == "jsonl" or output_path.suffix.lower() == ".jsonl":
        # One JSON record per line, with flattened claims + quote
        output_path.parent.mkdir(parents=True, exist_ok=True)
        with open(output_path, "w", encoding="utf-8") as f:
            for idx, rec in enumerate(verified_records, start=1):
                flat = {
                    "item_id": rec.item_id,
                    "source_uri": rec.source_uri,
                    "source_digest": rec.source_digest,
                    **rec.claims,
                    "primary_quote_text": rec.quotes[0].text if rec.quotes else "",
                    "quote_count": len(rec.quotes),
                    "quotes": [q.model_dump(mode="json") for q in rec.quotes],
                }
                if rank:
                    flat["rank"] = idx
                f.write(json.dumps(flat, ensure_ascii=False) + "\n")
    else:
        output_path.write_text(json.dumps(packet, indent=2), encoding="utf-8")

    return packet

