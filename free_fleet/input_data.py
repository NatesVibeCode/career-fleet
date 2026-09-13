"""Strict input boundary for JSON, JSONL, CSV, TXT, PDF, and HTML records."""
from __future__ import annotations

import csv
import json
import re
import html as _html
from html.parser import HTMLParser
from pathlib import Path
from typing import Optional

from pydantic import ValidationError

from .models import InputItem


class _TextExtractor(HTMLParser):
    def __init__(self):
        super().__init__()
        self._chunks: list[str] = []
        self._skip = False

    def handle_starttag(self, tag, attrs):
        if tag in ("script", "style", "noscript"):
            self._skip = True

    def handle_endtag(self, tag):
        if tag in ("script", "style", "noscript"):
            self._skip = False

    def handle_data(self, data):
        if not self._skip and data.strip():
            self._chunks.append(data.strip())

    def get_text(self) -> str:
        return "\n\n".join(self._chunks)


def _strip_html(text: str) -> str:
    parser = _TextExtractor()
    try:
        parser.feed(text)
        stripped = parser.get_text()
        return _html.unescape(stripped) if stripped else _html.unescape(re.sub(r"<[^>]+>", " ", text))
    except Exception:
        return _html.unescape(re.sub(r"<[^>]+>", " ", text))


def _extract_pdf_text(path: Path) -> str:
    """Best-effort PDF extraction using pypdf if available; falls back to raw bytes decode."""
    try:
        from pypdf import PdfReader  # type: ignore

        reader = PdfReader(str(path))
        pages = []
        for p in reader.pages:
            try:
                pages.append(p.extract_text() or "")
            except Exception:
                continue
        txt = "\n\n".join(pages).strip()
        if txt:
            return txt
    except Exception:
        pass
    # Fallback: try pdfminer.six
    try:
        from pdfminer.high_level import extract_text  # type: ignore

        txt = extract_text(str(path)) or ""
        if txt.strip():
            return txt.strip()
    except Exception:
        pass
    # Last resort: decode bytes and hint user
    raw = path.read_bytes()
    # If PDF header present, warn
    try:
        decoded = raw.decode("utf-8", errors="ignore")
        # Strip PDF binary artifacts crudely
        decoded = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f]", " ", decoded)
        if len(decoded.strip()) > 100:
            return decoded.strip()[:200_000]
    except Exception:
        pass
    raise ValueError(
        f"Cannot extract text from PDF '{path}': install 'pypdf' (pip install pypdf) or 'pdfminer.six' for robust extraction"
    )


class InputDataError(ValueError):
    pass


def _resolve_only_ids(only_ids: set[str] | list[str] | str | Path | None) -> set[str] | None:
    """Resolve an allowlist of item IDs from a set, comma-separated string, or file path."""
    if only_ids is None:
        return None
    if isinstance(only_ids, (set, list)):
        return {str(x).strip() for x in only_ids if str(x).strip()}

    candidate = None
    if isinstance(only_ids, Path):
        candidate = only_ids.expanduser()
    elif isinstance(only_ids, str) and not any(c in only_ids for c in ",;\n"):
        path = Path(only_ids).expanduser()
        try:
            if path.is_file() or path.suffix.lower() in {".csv", ".json", ".jsonl", ".txt"}:
                candidate = path
        except OSError:
            pass
    if candidate is not None and not candidate.is_file():
        raise InputDataError(f"ID filter file not found: {candidate}")

    if candidate and candidate.is_file():
        suffix = candidate.suffix.lower()
        if suffix == ".csv":
            with open(candidate, mode="r", encoding="utf-8-sig", errors="replace") as f:
                reader = csv.DictReader(f)
                if reader.fieldnames:
                    id_col = None
                    for c in ("item_id", "id", "domain", "key", "name", "slug"):
                        if c in reader.fieldnames:
                            id_col = c
                            break
                    id_col = id_col or reader.fieldnames[0]
                    return {(row.get(id_col) or "").strip() for row in reader if (row.get(id_col) or "").strip()}
        elif suffix == ".jsonl":
            ids = set()
            for line in candidate.read_text(encoding="utf-8-sig").splitlines():
                if line.strip():
                    try:
                        d = json.loads(line)
                        if isinstance(d, dict):
                            val = d.get("item_id") or d.get("id")
                            if val:
                                ids.add(str(val).strip())
                    except Exception:
                        pass
            return ids
        elif suffix == ".json":
            try:
                data = json.loads(candidate.read_text(encoding="utf-8-sig"))
                records = data.get("records") if isinstance(data, dict) and "records" in data else (data if isinstance(data, list) else [])
                ids = set()
                for rec in records:
                    if isinstance(rec, dict):
                        val = rec.get("item_id") or rec.get("id")
                        if val:
                            ids.add(str(val).strip())
                return ids
            except Exception:
                pass
        # Plain text: one ID per line
        return {line.strip() for line in candidate.read_text(encoding="utf-8-sig").splitlines() if line.strip()}

    if isinstance(only_ids, str):
        parts = [p.strip() for p in re.split(r"[,;\s]+", only_ids) if p.strip()]
        return set(parts) if parts else None

    return None


def load_input_items(
    path: str | Path,
    id_column: Optional[str] = None,
    text_column: Optional[str] = None,
    title_column: Optional[str] = None,
    uri_column: Optional[str] = None,
    only_ids: Optional[set[str] | list[str] | str | Path] = None,
) -> list[InputItem]:
    source = Path(path)
    if not source.is_file():
        raise InputDataError(f"input file not found: {source}")

    raw_items: object
    suffix = source.suffix.lower()
    if suffix == ".jsonl":
        rows: list[object] = []
        for line_number, line in enumerate(source.read_text(encoding="utf-8-sig").splitlines(), start=1):
            if not line.strip():
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise InputDataError(f"invalid JSONL at line {line_number}: {exc.msg}") from exc
        raw_items = rows
    elif suffix == ".json":
        try:
            raw_items = json.loads(source.read_text(encoding="utf-8-sig"))
        except json.JSONDecodeError as exc:
            raise InputDataError(f"invalid JSON: {exc.msg}") from exc
        if isinstance(raw_items, dict) and set(raw_items) == {"items"}:
            raw_items = raw_items["items"]
    elif suffix == ".csv":
        try:
            with open(source, mode="r", encoding="utf-8-sig", errors="replace") as f:
                reader = csv.DictReader(f)
                if not reader.fieldnames:
                    raise InputDataError("CSV file has no header columns")
                fieldnames = list(reader.fieldnames)
                
                # Resolve ID column
                resolved_id_col = id_column
                if not resolved_id_col:
                    for candidate in ("item_id", "id", "domain", "key", "name", "slug"):
                        if candidate in fieldnames:
                            resolved_id_col = candidate
                            break
                    if not resolved_id_col:
                        resolved_id_col = fieldnames[0]
                elif resolved_id_col not in fieldnames:
                    raise InputDataError(f"Specified id column '{resolved_id_col}' not found in CSV columns: {fieldnames}")

                # Resolve text column
                resolved_text_col = text_column
                if not resolved_text_col:
                    for candidate in ("text", "research", "content", "body", "description", "summary", "input"):
                        if candidate in fieldnames:
                            resolved_text_col = candidate
                            break
                    if not resolved_text_col:
                        non_id = [col for col in fieldnames if col != resolved_id_col]
                        if non_id:
                            resolved_text_col = non_id[0]
                        else:
                            raise InputDataError(f"CSV requires a text column; found only '{fieldnames[0]}'")
                elif resolved_text_col not in fieldnames:
                    raise InputDataError(f"Specified text column '{resolved_text_col}' not found in CSV columns: {fieldnames}")

                if title_column and title_column not in fieldnames:
                    raise InputDataError(f"Specified title column '{title_column}' not found in CSV columns: {fieldnames}")
                resolved_title_col = title_column if title_column in fieldnames else ("title" if "title" in fieldnames else None)

                if uri_column and uri_column not in fieldnames:
                    raise InputDataError(f"Specified uri column '{uri_column}' not found in CSV columns: {fieldnames}")
                resolved_uri_col = uri_column if uri_column in fieldnames else ("source_uri" if "source_uri" in fieldnames else ("url" if "url" in fieldnames else None))

                rows = []
                for row_idx, row in enumerate(reader, start=1):
                    # Skip completely empty rows (common in spreadsheet exports / trailing newlines)
                    if not any((v or "").strip() for v in row.values() if v is not None):
                        continue

                    raw_id = (row.get(resolved_id_col) or "").strip()
                    if not raw_id:
                        raise InputDataError(f"CSV row {row_idx} has empty ID column '{resolved_id_col}'")
                    cleaned_id = raw_id.replace(" ", "_").replace("/", "_").replace(":", "_")
                    
                    raw_text = (row.get(resolved_text_col) or "").strip()
                    if not raw_text:
                        continue
                    
                    title = row.get(resolved_title_col) if resolved_title_col else None
                    uri = row.get(resolved_uri_col) if resolved_uri_col else None
                    
                    meta = {
                        k: v for k, v in row.items()
                        if k not in (resolved_id_col, resolved_text_col, resolved_title_col, resolved_uri_col)
                        and v is not None and v != ""
                    }
                    rows.append({
                        "item_id": cleaned_id,
                        "text": raw_text,
                        "title": title or None,
                        "source_uri": uri or None,
                        "metadata": meta,
                    })
                raw_items = rows
        except Exception as exc:
            if isinstance(exc, InputDataError):
                raise
            raise InputDataError(f"failed to parse CSV: {exc}") from exc
    elif suffix in (".txt", ".md"):
        txt = source.read_text(encoding="utf-8", errors="replace")
        raw_items = [{"item_id": source.stem.replace(" ", "_"), "text": txt, "title": source.name}]
    elif suffix in (".html", ".htm"):
        html = source.read_text(encoding="utf-8", errors="replace")
        txt = _strip_html(html)
        if not txt.strip():
            raise InputDataError(f"HTML file '{source}' produced no extractable text")
        raw_items = [{"item_id": source.stem.replace(" ", "_"), "text": txt, "title": source.name}]
    elif suffix == ".pdf":
        txt = _extract_pdf_text(source)
        raw_items = [{"item_id": source.stem.replace(" ", "_"), "text": txt, "title": source.name}]
    else:
        raise InputDataError("input must use .json, .jsonl, .csv, .txt, .md, .html, or .pdf")

    if not isinstance(raw_items, list):
        raise InputDataError("input must be an array, an {items: [...]} object, or JSONL/CSV rows")
    if not raw_items:
        raise InputDataError("input contains no items")

    items: list[InputItem] = []
    seen: set[str] = set()
    for index, raw_item in enumerate(raw_items):
        try:
            item = InputItem.model_validate(raw_item)
        except ValidationError as exc:
            raise InputDataError(f"invalid item {index}: {exc.errors(include_url=False)}") from exc
        if item.item_id in seen:
            raise InputDataError(f"duplicate item_id: {item.item_id}")
        seen.add(item.item_id)
        items.append(item)

    target_ids = _resolve_only_ids(only_ids)
    if target_ids is not None:
        if suffix == ".csv":
            target_ids = {value.replace(" ", "_").replace("/", "_").replace(":", "_") for value in target_ids}
        items = [
            item for item in items
            if item.item_id in target_ids
            or item.item_id.replace("_", " ") in target_ids
            or item.item_id.replace(" ", "_") in target_ids
        ]
    return items
