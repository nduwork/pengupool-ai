#!/usr/bin/env python3
"""PenguPool status detector for the every-minute hook.

Polls the PenguPool daemon on the owner's workstation through the API
only, compares against the last recorded state, and prints exactly one
JSON line: {"decision": "silent"|"wake", "reason": str, "payload": {...}}.

Signals, all from the paired-device API (pp.py): per-session presence and
status (/sessions), a pending ask (/next), and the daemon epoch
(/api/healthz — a changed epoch means the daemon restarted, the signal
the detector used to get from settings.get's version). State lives in
~/hooks/state/pengupool-status.json and is NOT written on dry runs.
"""
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pp  # noqa: E402  (the device-API client)

STATE_PATH = os.path.expanduser("~/hooks/state/pengupool-status.json")
CONFIG_PATH = os.path.expanduser("~/workspace/companion/config.json")


def load_config():
    try:
        with open(CONFIG_PATH) as f:
            cfg = json.load(f)
    except Exception:
        cfg = {}
    return {"enabled": bool(cfg.get("enabled", True)),
            "interval_secs": int(cfg.get("interval_secs", 60))}
DRY = os.environ.get("HATCH_HOOK_DRY_RUN") == "1"

def observe():
    """obs: {sessions: {id: {name, status}}, ask, ask_detail}; sessions stays empty when unreachable."""
    obs = {"sessions": {}, "ask": False, "refused": False, "reachable": False,
           "epoch": None}
    try:
        code, res = pp.api("GET", "/sessions")
        obs["refused"] = code == 401
        obs["reachable"] = code == 200  # an empty list is still an answer
        if code == 200:
            for s in res or []:
                st = "waiting on you" if s.get("waiting") else s.get("status") or s.get("state", "?")
                obs["sessions"][s["session"]] = {"name": s.get("name") or s.get("title", "?"), "status": st}
        # /next answers 200 with the ask card exactly when an ask waits on the owner (204: none).
        code, card = pp.api("GET", "/next")
        obs["ask"] = code == 200
        if code == 200 and card:
            obs["ask_detail"] = {k: card.get(k) for k in ("session", "title", "description")}
    except (OSError, EOFError, ValueError, SystemExit):
        pass
    try:
        code, info = pp.healthz()
        if code == 200 and info:
            obs["epoch"] = info.get("epoch")
    except (OSError, EOFError, ValueError, SystemExit):
        pass
    return obs


def main():
    cfg = load_config()
    if not cfg["enabled"]:
        print(json.dumps({"decision": "silent", "reason": "monitor disabled by config",
                          "payload": {"changes": []}}))
        return 0

    prev = None
    if os.path.exists(STATE_PATH):
        try:
            with open(STATE_PATH) as f:
                prev = json.load(f)
        except Exception:
            prev = None

    last = (prev or {}).get("last_check_epoch", 0)
    if not os.environ.get("COMPANION_FORCE") and time.time() - last < cfg["interval_secs"]:
        print(json.dumps({"decision": "silent",
                          "reason": f"not due (interval {cfg['interval_secs']}s)",
                          "payload": {"changes": []}}))
        return 0

    obs = observe()
    reachable = obs["reachable"]
    changes = []

    if prev is None:
        decision, reason = "silent", "baseline recorded"
    else:
        old, new = prev.get("sessions", {}), obs["sessions"]
        if reachable:
            for sid, s in new.items():
                if sid not in old:
                    changes.append(f"session appeared: {s['name']} ({s['status']})")
                elif old[sid].get("status") != s["status"]:
                    changes.append(
                        f"{s['name']}: {old[sid].get('status')} -> {s['status']}"
                    )
            for sid, s in old.items():
                if sid not in new:
                    changes.append(f"session gone: {s['name']}")
            if obs["ask"] and not prev.get("ask"):
                d = obs.get("ask_detail") or {}
                who = d.get("title") or d.get("session") or "a session"
                what = (d.get("description") or "").strip()
                if len(what) > 140:
                    what = what[:137] + "..."
                changes.append(f"needs input: {who}" + (f" — {what}" if what else ""))
            if obs.get("epoch") and prev.get("epoch") and obs["epoch"] != prev["epoch"]:
                changes.append("daemon restarted (epoch changed)")
        if obs["refused"] and not prev.get("refused"):
            changes.append("PenguPool refused the device token (revoked?); pair this device again")
        if reachable and not prev.get("reachable", True):
            changes.append("daemon reachable again")
        if not reachable and not obs["refused"] and prev.get("reachable", True):
            changes.append("daemon unreachable via API")
        decision = "wake" if changes else "silent"
        reason = "; ".join(changes) if changes else "no material change"

    state = {
        "sessions": obs["sessions"],
        "ask": obs["ask"],
        "reachable": reachable,
        "refused": obs["refused"],
        "epoch": obs.get("epoch") or (prev or {}).get("epoch"),
        "last_check_epoch": time.time(),
    }
    if not DRY:
        os.makedirs(os.path.dirname(STATE_PATH), exist_ok=True)
        tmp = STATE_PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump(state, f)
        os.replace(tmp, STATE_PATH)

    print(json.dumps({"decision": decision, "reason": reason,
                      "payload": {"changes": changes, "obs": obs}}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
