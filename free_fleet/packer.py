"""Typed item batching and prompt packaging."""
import hashlib
import json
from typing import Any

from .models import InputItem, PackedBatch
from .slicer import slice_document

def pack_items(
    raw_records: list[InputItem | dict[str, Any]],
    batch_size: int = 6,
    max_slice_chars: int = 6000
) -> list[dict[str, Any]]:
    """Transforms raw records into sliced cards and packs them into bounded batches."""
    if batch_size <= 0:
        raise ValueError("batch_size must be greater than 0")
    cards = []
    for raw_record in raw_records:
        record = raw_record if isinstance(raw_record, InputItem) else InputItem.model_validate(raw_record)
        iid = record.item_id
        text = record.text
        title = record.title
        
        slices = slice_document(text, max_chars=max_slice_chars)
        cards.append({
            "item_id": iid,
            "title": title,
            "source_uri": record.source_uri,
            "content_type": record.content_type,
            "metadata": record.metadata,
            "source_digest": hashlib.sha256(text.encode()).hexdigest(),
            "slices": slices,
            "full_char_length": len(text)
        })

    batches = []
    for i in range(0, len(cards), batch_size):
        chunk = cards[i:i + batch_size]
        batch_hash = hashlib.sha256(json.dumps([c["item_id"] for c in chunk], sort_keys=True).encode()).hexdigest()[:16]
        batch = PackedBatch.model_validate({
            "batch_id": f"batch_{batch_hash}",
            "items": chunk
        })
        batches.append(batch.model_dump(mode="json"))

    return batches
