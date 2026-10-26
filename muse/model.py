"""One OpenAI-compatible chat client for every Muse model (Meta Model API, Baseten, Modal, Ollama)."""

import json
import os
import re
import urllib.request


class ModelError(Exception):
    pass


def chat(messages, model=None, timeout=60):
    base = os.environ.get("MUSE_BASE_URL", "").rstrip("/")
    if not base:
        raise ModelError("set MUSE_BASE_URL (and MUSE_API_KEY, MUSE_MODEL)")
    body = {"model": model or os.environ.get("MUSE_MODEL", ""), "messages": messages, "temperature": 0}
    req = urllib.request.Request(base + "/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json",
                                          "Authorization": "Bearer " + os.environ.get("MUSE_API_KEY", "")})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.load(r)["choices"][0]["message"]["content"]
    except (OSError, KeyError, IndexError, ValueError) as e:
        raise ModelError(str(e)) from e


def ask_json(system, user, model=None):
    """Ask for a JSON object; tolerate prose or code fences around it."""
    text = chat([{"role": "system", "content": system}, {"role": "user", "content": user}], model)
    m = re.search(r"\{.*\}", text, re.S)
    if not m:
        raise ModelError("no JSON object in the answer: " + text[:200])
    try:
        return json.loads(m.group(0))
    except ValueError as e:
        raise ModelError("bad JSON in the answer: " + text[:200]) from e
