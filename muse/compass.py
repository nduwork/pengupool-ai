"""The Compass: one JSON-lines file, each line a rule the owner confirmed."""

import json
import os
import time
import uuid
from pathlib import Path


def path():
    return Path(os.environ.get("PENGUPOOL_MUSE_HOME", Path.home() / ".pengupool-muse")) / "compass.jsonl"


def load():
    p = path()
    if not p.exists():
        return []
    return [json.loads(l) for l in p.read_text().splitlines() if l.strip()]


def add(rule, source):
    line = {"id": uuid.uuid4().hex[:8], "rule": rule, "source": source,
            "created": time.strftime("%Y-%m-%dT%H:%M:%S%z")}
    p = path()
    p.parent.mkdir(parents=True, exist_ok=True)
    with p.open("a") as f:
        f.write(json.dumps(line) + "\n")
    return line


def forget(line_id):
    lines = load()
    keep = [l for l in lines if l["id"] != line_id]
    if len(keep) == len(lines):
        return False
    path().write_text("".join(json.dumps(l) + "\n" for l in keep))
    return True
