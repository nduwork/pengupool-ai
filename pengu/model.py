"""One OpenAI-compatible chat client for every Muse model (Meta Model API, Baseten, Modal, Ollama)."""

import json
import os
import re
import urllib.request


class ModelError(Exception):
    pass


# Meta Model API by default; MUSE_* point the same client at Baseten, Modal or Ollama.
def chat(messages, model=None, timeout=60, **extra):
    base = os.environ.get("MUSE_BASE_URL", "https://api.meta.ai/v1").rstrip("/")
    key = os.environ.get("MUSE_API_KEY") or os.environ.get("META_API", "")
    body = {"model": model or os.environ.get("MUSE_MODEL", "muse-spark-1.3"), "messages": messages, **extra}
    req = urllib.request.Request(base + "/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", "Authorization": "Bearer " + key})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.load(r)["choices"][0]["message"]["content"]
    except (OSError, KeyError, IndexError, ValueError) as e:
        raise ModelError(str(e)) from e


def ask_json(system, user, model=None, schema=None):
    """Ask for a JSON object, held to `schema` when given (the Meta Model API enforces it strictly);
    tolerate prose or code fences around it for endpoints that don't."""
    extra = {"response_format": {"type": "json_schema", "json_schema": {"name": "answer", "strict": True, "schema": schema}}} if schema else {}
    text = chat([{"role": "system", "content": system}, {"role": "user", "content": user}], model, **extra)
    m = re.search(r"\{.*\}", text, re.S)
    if not m:
        raise ModelError("no JSON object in the answer: " + text[:200])
    try:
        return json.loads(m.group(0))
    except ValueError as e:
        raise ModelError("bad JSON in the answer: " + text[:200]) from e
