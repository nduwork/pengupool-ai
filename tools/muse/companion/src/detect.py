#!/usr/bin/env python3
"""PenguPool status detector for the every-minute hook.

Polls the PenguPool daemon on the owner's workstation through the API
only, compares against the last recorded state, and prints exactly one
JSON line: {"decision": "silent"|"wake", "reason": str, "payload": {...}}.

Signals: per-session presence + status (owner /ws session.list, falling
back to the device API), a device-visible pending ask (device /next),
and the daemon version (settings.get). State lives in
~/hooks/state/pengupool-status.json and is NOT written on dry runs.
"""
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pp  # noqa: E402  (the /ws owner-API client)

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

def device_api(path):
    """One GET on the device API over the CONNECT tunnel. Returns (status, body)."""
    tok_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "device-token")
    with open(tok_path) as f:
        tok = f.read().strip()
    sk = pp.tunnel()
    req = (
        f"GET /api/muse/v1{path} HTTP/1.1\r\n"
        f"Host: {pp.HOST}:{pp.PORT}\r\n"
        f"Authorization: Bearer {tok}\r\nConnection: close\r\n\r\n"
    )
    sk.s.sendall(req.encode())
    head = sk.read_until(b"\r\n\r\n").decode("latin1")
    status = int(head.split(" ", 2)[1])
    headers = {}
    for line in head.split("\r\n")[1:]:
        if ": " in line:
            k, v = line.split(": ", 1)
            headers[k.lower()] = v
    if "chunked" in headers.get("transfer-encoding", ""):
        body = bytearray()
        while True:
            size_line = sk.read_until(b"\r\n").decode("latin1").strip()
            n = int(size_line.split(";")[0], 16)
            if n == 0:
                sk.read_until(b"\r\n")
                break
            body += sk.read_exact(n)
            sk.read_until(b"\r\n")
        return status, bytes(body)
    n = int(headers.get("content-length", "0"))
    return status, sk.read_exact(n) if n else b""


def observe():
    """Returns (obs, ws_ok). obs: {sessions:{id:{name,status}}, version, ask}"""
    obs = {"sessions": {}, "version": None, "ask": False}
    ws_ok = False
    try:
        c = pp.Client()
        c.hello()
        m = c.call("session.list")
        for s in m.get("sessions", []):
            obs["sessions"][s["id"]] = {
                "name": s.get("name", "?"),
                "status": s.get("status") or s.get("state") or "?",
            }
        st = c.call("settings.get")
        res = st.get("result", st)
        obs["version"] = res.get("version")
        c.close()
        ws_ok = True
    except Exception:
        ws_ok = False
    # Device API: session fallback + the pending-ask signal (204 = none).
    # The ask is a direct signal from the host, not something to infer:
    # /next answers 200 with the ask card (session, title, description)
    # exactly when a session is waiting on the owner.
    try:
        code, body = device_api("/next")
        obs["ask"] = code == 200
        if code == 200:
            try:
                card = json.loads(body)
                obs["ask_detail"] = {
                    "session": card.get("session"),
                    "title": card.get("title"),
                    "description": card.get("description"),
                }
            except Exception:
                obs["ask_detail"] = None
        if not obs["sessions"]:
            code2, body2 = device_api("/sessions")
            if code2 == 200:
                for s in json.loads(body2):
                    obs["sessions"][s["session"]] = {
                        "name": s.get("title", "?"),
                        "status": "waiting" if s.get("waiting") else s.get("state", "?"),
                    }
    except Exception:
        pass
    return obs, ws_ok


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

    obs, ws_ok = observe()
    reachable = bool(obs["sessions"]) or obs["ask"]
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
            if obs["version"] and prev.get("version") and obs["version"] != prev["version"]:
                changes.append(f"daemon version {prev['version']} -> {obs['version']}")
        if reachable and not prev.get("reachable", True):
            changes.append("daemon reachable again")
        if not reachable and prev.get("reachable", True):
            changes.append("daemon unreachable via API")
        if not ws_ok and reachable and not prev.get("ws_stale"):
            changes.append("bootstrap token stale (daemon restarted?); refresh needed")
        decision = "wake" if changes else "silent"
        reason = "; ".join(changes) if changes else "no material change"

    state = {
        "sessions": obs["sessions"],
        "version": obs["version"] or (prev or {}).get("version"),
        "ask": obs["ask"],
        "reachable": reachable,
        "ws_stale": (not ws_ok) and reachable,
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
