"""Abstract base provider interface."""
import json
import re
from abc import ABC, abstractmethod
from typing import Any

from ..models import ProviderReceipt, RoutePolicy


def clean_llm_json(text: str | None) -> Any | None:
    """Robustly extracts and parses JSON from LLM responses.
    
    Handles:
    - Pure JSON strings
    - Markdown fenced blocks (```json ... ``` or ``` ... ```) anywhere in the text
    - Conversational preambles and postambles (e.g. 'Here is the JSON: ... Hope this helps!')
    - Trailing commas before closing braces/brackets
    """
    if not text:
        return None
    text = text.strip()

    # 1. Direct parse
    try:
        return json.loads(text)
    except Exception:
        pass

    # 2. Markdown code fences anywhere in the output
    fenced = re.search(r"```(?:json)?\s*(.*?)\s*```", text, re.DOTALL)
    if fenced:
        cand = fenced.group(1).strip()
        try:
            return json.loads(cand)
        except Exception:
            pass
        cand_fixed = re.sub(r",\s*([\]}])", r"\1", cand)
        try:
            return json.loads(cand_fixed)
        except Exception:
            pass

    # 3. First parseable JSON value: scan every bracket with raw_decode.
    # First-to-last spanning glues preamble braces onto the payload
    # ("Here is {an idea}: {...}"), so decode positionally instead.
    decoder = json.JSONDecoder()
    for match in re.finditer(r"[{[]", text):
        try:
            cand, _ = decoder.raw_decode(text[match.start():])
        except json.JSONDecodeError:
            continue
        if isinstance(cand, (dict, list)):
            return cand
        # A bare scalar is not a model answer; keep scanning.
    # 4. Trailing-comma repair on the same positional scan.
    for match in re.finditer(r"[{[]", text):
        tail = text[match.start():]
        fixed = re.sub(r",\s*([\]}])", r"\1", tail)
        try:
            cand, _ = decoder.raw_decode(fixed)
        except json.JSONDecodeError:
            continue
        if isinstance(cand, (dict, list)):
            return cand

    return None

# Error-body signals that a 400 rejected the constraint mechanism itself (not
# some unrelated parameter). Deliberately narrow: generic words like
# "parameter" or "schema" appear in unrelated 400s and must not trigger an
# unconstrained retry.
SCHEMA_REJECTION_KEYWORDS = (
    "response_format",
    "json_schema",
    "structured output",
    "structured_output",
)


def extract_task_payload(prompt: str) -> dict | None:
    """Return the task payload ({input_items, output_schema}) embedded in a prompt.

    Scans every ``{`` with raw_decode and returns the first object containing
    the task keys. Profile context and instructions may precede the payload
    with their own braces, so anchoring on the first ``{`` silently misses
    the schema and disables constrained decoding.
    """
    decoder = json.JSONDecoder()
    cursor = 0
    while True:
        first_brace = prompt.find("{", cursor)
        if first_brace == -1:
            return None
        try:
            candidate, _ = decoder.raw_decode(prompt[first_brace:])
        except json.JSONDecodeError:
            cursor = first_brace + 1
            continue
        if isinstance(candidate, dict) and {"input_items", "output_schema"}.issubset(candidate):
            return candidate
        cursor = first_brace + 1

class BaseProvider(ABC):
    @abstractmethod
    def run_prompt(
        self,
        route_id: str,
        prompt: str,
        system_prompt: str | None = None,
        timeout_sec: int = 120,
        session_id: str | None = None,
        policy: RoutePolicy | None = None,
    ) -> tuple[bool, str | None, ProviderReceipt]:
        """Executes a prompt in a session. Returns (success, text_response, receipt)."""
        pass
