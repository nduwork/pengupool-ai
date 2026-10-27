"""The Compass: one JSON-lines file, each line a rule the owner confirmed."""

import json
import os
import time
import uuid
from pathlib import Path


def path():
    return Path(os.environ.get("PENGUPOOL_MUSE_HOME") or Path.home() / ".pengupool-muse") / "compass.jsonl"


def normalize(line):
    """Old lines (id/rule/source/created) gain evidence, confidence, updated."""
    line.setdefault("evidence", [])
    line.setdefault("confidence", 1.0)
    line.setdefault("updated", line.get("created", ""))
    if not isinstance(line["evidence"], list):
        line["evidence"] = []
    try:
        line["confidence"] = max(0.0, min(1.0, float(line["confidence"])))
    except (TypeError, ValueError):
        line["confidence"] = 1.0
    return line


def load():
    p = path()
    if not p.exists():
        return []
    lines = []
    for raw in p.read_text(encoding="utf-8", errors="replace").splitlines():
        if not raw.strip():
            continue
        try:
            line = json.loads(raw)
        except ValueError:
            continue  # a half-written line never breaks the store
        if isinstance(line, dict) and isinstance(line.get("rule"), str):
            lines.append(normalize(line))
    return lines


def format_lines(lines):
    """One wording for Compass lines everywhere (MCP, task prefix, resources)."""
    return "\n".join("- %s" % l.get("rule", "") for l in lines)


def add(rule, source, confidence=1.0):
    from . import session
    now = time.time()
    line = {"id": uuid.uuid4().hex[:12], "rule": rule, "source": source,
            "created": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "evidence": [], "confidence": confidence, "updated": now}
    with session._locked():
        p = path()
        p.parent.mkdir(parents=True, exist_ok=True)
        with p.open("a") as f:
            f.write(json.dumps(line) + "\n")
    return line


def _save_all(lines):
    tmp = path().with_suffix(".tmp")
    tmp.write_text("".join(json.dumps(l) + "\n" for l in lines), encoding="utf-8")
    os.replace(tmp, path())  # no silent fallback: a failed save must raise


def update_rule(line_id, text):
    """Owner-confirmed rewording. Returns True when the line exists."""
    from . import session
    with session._locked():
        lines = load()
        for line in lines:
            if line.get("id") == line_id:
                line["rule"] = text
                line["updated"] = time.time()
                _save_all(lines)
                return True
    return False


def update_evidence(line_id, effect, words, session_id, now=None):
    """Support raises confidence, contradiction lowers it; age discounts first.
    Returns the updated line, or None."""
    if effect not in ("supports", "contradicts"):
        raise ValueError("effect is supports or contradicts")
    from . import session
    now = now if now is not None else time.time()
    with session._locked():
        lines = load()
        for line in lines:
            if line.get("id") != line_id:
                continue
            age_days = max(0.0, now - _ts(line.get("updated"), now)) / 86400
            conf = line.get("confidence", 1.0) * 0.5 ** (age_days / 90.0)
            if effect == "supports":
                conf = min(1.0, conf + 0.15)
            elif effect == "contradicts":
                conf = max(0.0, conf - 0.30)
            line["confidence"] = conf
            line["updated"] = now
            line.setdefault("evidence", []).append(
                {"at": now, "words": words, "session": session_id, "effect": effect})
            _save_all(lines)
            return line
    return None


def _ts(updated, now):
    if isinstance(updated, (int, float)):
        return updated
    try:
        return time.mktime(time.strptime(updated, "%Y-%m-%dT%H:%M:%S%z"))
    except (TypeError, ValueError, OverflowError):
        return now


def forget(line_id):
    from . import session
    with session._locked():
        lines = load()
        keep = [l for l in lines if l["id"] != line_id]
        if len(keep) == len(lines):
            return False
        _save_all(keep)
    return True
