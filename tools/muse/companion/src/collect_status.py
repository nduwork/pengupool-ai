#!/usr/bin/env python3
"""One-shot PenguPool status collector for the live-status artifact.

Reads the daemon through the paired device API (pp.py client in this
same directory): /sessions for the team list and tree, /next for the
pending ask. Prints ONE compact JSON document with only the fields
the status page displays. Tokens never leave this process.

The gateway's /ws owner surface is local-only now, so the fields it
used to supply (daemon version, context %, activity times, cwd) are
null here; the page renders without them.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pp  # noqa: E402


def main():
    raw = pp.sessions()
    slim_sessions = []
    for s in raw:
        slim_sessions.append({
            "id": s.get("session"),
            "name": s.get("name") or "?",
            "harness": "claude",
            "state": s.get("state"),
            "status": s.get("status"),
            "context_pct": None,
            "active_at": None,
            "cwd": None,
        })
    teams = {}
    for s in raw:
        tid = s.get("team_id") or ""
        t = teams.setdefault(tid, {"name": s.get("team") or "team",
                                   "state": "active", "members": []})
        t["members"].append({
            "session_id": s.get("session"),
            "role": "main" if s.get("role") == "LEAD" else "helper",
            "parent": s.get("parent"),
            "state": "active",
        })
    checkins = {s["session"] for s in raw if s.get("waiting")}
    try:
        card = pp.next_card()
        if card and card.get("session"):
            checkins.add(card["session"])
    except Exception:
        pass
    print(json.dumps({
        "sessions": slim_sessions,
        "teams": list(teams.values()),
        "version": None,
        "checkins": sorted(checkins),
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
