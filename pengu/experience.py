"""M4 experience: per-helper lessons in JSON-lines, separate from the Compass."""
import hashlib
import json
import os
import re
import sqlite3
import time
import uuid
from pathlib import Path

from . import judge, session
from .judge import redact  # one redactor for stored lessons and judge prompts

THRESHOLD = 0.6
TOP_K = 5

def path(helper):
    safe = re.sub(r"[^a-z0-9_-]", "", helper.lower()) or "helper"
    digest = hashlib.sha1(helper.encode()).hexdigest()[:8]
    return session.home() / f"experience-{safe}-{digest}.jsonl"


def load(helper):
    p = path(helper)
    if not p.exists():
        return []
    out = []
    for raw in p.read_text(encoding="utf-8", errors="replace").splitlines():
        if not raw.strip():
            continue
        try:
            e = json.loads(raw)
        except ValueError:
            continue
        if (isinstance(e, dict) and isinstance(e.get("id"), str)
                and isinstance(e.get("lesson"), str)):
            out.append(e)
    return out


def save(helper, title, task, lesson, outcome, session_id):
    """Store one entry verbatim (already generalized or redacted by the caller)."""
    if outcome not in ("worked", "failed"):
        raise ValueError("outcome is worked or failed")
    e = {"id": uuid.uuid4().hex[:12], "helper": helper, "title": redact(title),
         "task": redact(task), "lesson": redact(lesson),
         "evidence": [{"at": time.time(), "session": session_id, "outcome": outcome}],
         "uses": 0, "last_used": 0}
    p = path(helper)
    p.parent.mkdir(parents=True, exist_ok=True)
    with session._locked():
        with open(p, "a", encoding="utf-8") as f:
            f.write(json.dumps(e) + "\n")
    return e


def _append_evidence(entry, outcome, session_id, effect="supports"):
    entry.setdefault("evidence", []).append(
        {"at": time.time(), "session": session_id, "outcome": outcome, "effect": effect})


def ingest(helper, title, task, summary, outcome, session_id, model=None):
    """End of task: generalize the compacted summary, match existing lessons.
    merge adds evidence; contradict weakens with counter-evidence; a supports
    match below 0.85 confidence refines (the fresh generalization rewords);
    otherwise the entry is new."""
    if outcome not in ("worked", "failed"):
        raise ValueError("outcome is worked or failed")
    from . import wording
    lesson = judge.redact(wording.generalize_lesson(summary, model=model) or "")
    if not lesson.strip():
        lesson = judge.redact(summary)
    if not lesson.strip():
        raise ValueError("empty lesson")
    entries = load(helper)
    best, best_rel, best_conf = None, None, -1.0
    for e in _candidates(entries, lesson, limit=5):
        try:
            r = judge.relate(lesson, e.get("lesson", ""), model=model)
        except ValueError:
            continue
        if r["relation"] != "unrelated" and r["confidence"] > best_conf:
            best, best_rel, best_conf = e, r["relation"], r["confidence"]
    if best is None:
        return {"action": "new", "entry": save(helper, title, task, lesson, outcome, session_id)}
    p = path(helper)
    with session._locked():
        fresh = load(helper)
        matched = next((e for e in fresh if e["id"] == best["id"]), None)
        if matched is None:
            need_new = True
        else:
            need_new = False
            if best_rel == "contradicts":
                _append_evidence(matched, outcome, session_id, effect="contradicts")
                action = "contradict"
            elif best_rel == "supports" and best_conf >= 0.85:
                _append_evidence(matched, outcome, session_id)
                action = "merge"
            else:
                matched["lesson"] = lesson
                _append_evidence(matched, outcome, session_id)
                action = "refine"
            tmp = p.with_suffix(".tmp")
            tmp.write_text("".join(json.dumps(x) + "\n" for x in fresh), encoding="utf-8")
            os.replace(tmp, p)
    if need_new:
        # entry vanished between match and write: store fresh instead.
        return {"action": "new",
                "entry": save(helper, title, task, lesson, outcome, session_id)}
    return {"action": action, "entry": matched}


def _candidates(entries, task, limit=20):
    """FTS5 over title+task+lesson, transient (the jsonl file is the store)."""
    if not entries:
        return []
    terms = list(dict.fromkeys(re.findall(r"[A-Za-z][A-Za-z0-9_-]{2,}", task.lower())))[:12]
    if not terms:
        return entries[-limit:]
    db = None
    try:
        db = sqlite3.connect(":memory:")
        db.execute("CREATE VIRTUAL TABLE e USING fts5(id UNINDEXED, text)")
        for x in entries:
            db.execute("INSERT INTO e VALUES (?, ?)",
                       (x["id"], f"{x.get('title','')} {x.get('task','')} {x.get('lesson','')}"))
        q = " OR ".join(f'"{t}"' for t in terms)
        rows = db.execute("SELECT id FROM e WHERE e MATCH ? ORDER BY rank LIMIT ?",
                          (q, limit)).fetchall()
    except sqlite3.Error:
        return entries[-limit:]  # no FTS5 here: newest first
    finally:
        if db is not None:
            db.close()
    by_id = {x["id"]: x for x in entries}
    return [by_id[r[0]] for r in rows if r[0] in by_id]


def _evidences(e):
    evs = e.get("evidence")
    return evs if isinstance(evs, list) else []


def _recency(e, now):
    last = 0
    for ev in _evidences(e):
        if not isinstance(ev, dict):
            continue
        try:
            last = max(last, float(ev.get("at", 0)))
        except (TypeError, ValueError, OverflowError):
            pass
    days = max(0.0, (now - last) / 86400) if last else 365.0
    return 1.0 / (1.0 + days / 30.0)


def _outcome_factor(e):
    evs = [ev for ev in _evidences(e) if isinstance(ev, dict)]
    worked = sum(1 for ev in evs if ev.get("outcome") == "worked")
    failed = sum(1 for ev in evs if ev.get("outcome") == "failed")
    contradicted = sum(1 for ev in evs if ev.get("effect") == "contradicts")
    if not evs:
        return 1.0
    base = 1.0 if worked >= failed else 0.5
    return max(0.1, base * (0.5 ** contradicted))  # each contradiction halves, floor 0.1


def for_task(helper, task, k=TOP_K, threshold=THRESHOLD, model=None):
    """<=k entries: FTS5 candidates, judge relevance, code ranking. Stamps use."""
    k = max(0, k)
    if k == 0:
        return []  # no model call for an empty ask
    scrubbed = redact(task)  # FTS runs on the original; only the model sees the scrubbed text
    entries = load(helper)
    cands = _candidates(entries, task)
    if not cands:
        return []
    rated = {r["id"]: r for r in judge.rate_relevance(scrubbed, cands, model=model)}
    now = time.time()
    ranked = []
    for c in cands:
        r = rated.get(c["id"])
        if not r or not r["relevant"] or r["confidence"] < threshold:
            continue
        ranked.append((r["confidence"] * _outcome_factor(c) * _recency(c, now), c))
    ranked.sort(key=lambda t: -t[0])
    top = [c for _, c in ranked[:k]]
    if top:
        ids = {c["id"] for c in top}
        with session._locked():
            fresh = []
            for e in load(helper):
                if e["id"] in ids:
                    e["uses"] = (e["uses"] if isinstance(e.get("uses"), int) else 0) + 1
                    e["last_used"] = now
                fresh.append(e)
            tmp = path(helper).with_suffix(".tmp")
            tmp.write_text("".join(json.dumps(x) + "\n" for x in fresh), encoding="utf-8")
            os.replace(tmp, path(helper))  # atomic: a crash never empties the store
        stamped = {e["id"]: e for e in fresh if e["id"] in ids}
        top = [stamped[c["id"]] for c in top]  # callers see the stamped rows
    return top
