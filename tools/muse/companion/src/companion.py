#!/usr/bin/env python3
"""companion — CLI for Muse's companion monitor on this VM.

Manages the PenguPool status monitor without code changes:
  companion status                 config + last observed team state
  companion config                 show the monitor config
  companion config set interval 5m set the check timer (90 | 90s | 5m | 1h)
  companion config set enabled off pause / on resume
  companion check                  run one check right now
  companion log [n]                the last n poll decisions
  companion gadget [--out PATH]    render the team status card (chat widget HTML)
  companion tracker [--out PATH]   render the work-tracker card (chat widget HTML)
  companion ask SPEC [--answer T]  render an answer card for a pending question

The every-minute hook polls the detector; the detector reads
~/workspace/companion/config.json on every poll and only performs a
check when the configured interval has elapsed. Changing the timer is
writing this config — nothing is re-registered or redeployed.
"""
import json
import os
import re
import subprocess
import sys
import time

COMPANION_DIR = os.path.dirname(os.path.abspath(__file__))
CONFIG_PATH = os.path.join(COMPANION_DIR, "config.json")
STATE_PATH = os.path.expanduser("~/hooks/state/pengupool-status.json")
HOOK_LOG = os.path.expanduser("~/hooks/logs/pengupool-status.jsonl")
DETECT = os.path.expanduser("~/workspace/pengupool-agent/detect.py")
GADGET = os.path.expanduser("~/workspace/pengupool-agent/gadget.py")
TRACKER = os.path.expanduser("~/workspace/pengupool-agent/tracker.py")
ASK = os.path.expanduser("~/workspace/pengupool-agent/askcard.py")
BASE_POLL_SECS = 60  # the hook's own cadence; the effective floor


def load_config():
    try:
        with open(CONFIG_PATH) as f:
            cfg = json.load(f)
    except Exception:
        cfg = {}
    return {"enabled": bool(cfg.get("enabled", True)),
            "interval_secs": int(cfg.get("interval_secs", 60))}


def save_config(cfg):
    tmp = CONFIG_PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(cfg, f, indent=2)
    os.replace(tmp, CONFIG_PATH)


def fmt_interval(secs):
    if secs % 3600 == 0:
        return f"{secs // 3600}h"
    if secs % 60 == 0:
        return f"{secs // 60}m"
    return f"{secs}s"


def parse_interval(text):
    m = re.fullmatch(r"(\d+)\s*([smh]?)", text.strip().lower())
    if not m:
        return None
    n, unit = int(m.group(1)), m.group(2) or "s"
    return n * {"s": 1, "m": 60, "h": 3600}[unit]


def cmd_status():
    cfg = load_config()
    print(f"monitor: {'enabled' if cfg['enabled'] else 'PAUSED'}"
          f" | check interval: {fmt_interval(cfg['interval_secs'])}"
          f" (poll every {BASE_POLL_SECS}s)")
    try:
        with open(STATE_PATH) as f:
            st = json.load(f)
    except Exception:
        print("no check has run yet")
        return
    last = st.get("last_check_epoch")
    if last:
        ago = int(time.time() - last)
        print(f"last check: {ago}s ago | reachable: {st.get('reachable')}"
              f" | ask waiting: {st.get('ask')}")
    for s in st.get("sessions", {}).values():
        print(f"  {s['name']}: {s['status']}")


def cmd_config(args):
    if not args:
        print(json.dumps(load_config(), indent=2))
        return
    if args[0] == "set" and len(args) >= 3:
        key, val = args[1], args[2]
        cfg = load_config()
        if key == "interval":
            secs = parse_interval(val)
            if secs is None or secs <= 0:
                print(f"cannot parse interval: {val!r} (try 90, 5m, 1h)")
                sys.exit(2)
            cfg["interval_secs"] = secs
            save_config(cfg)
            note = ""
            if secs < BASE_POLL_SECS:
                note = f" (the poll floor is {BASE_POLL_SECS}s, so checks run at that cadence)"
            print(f"check interval set to {fmt_interval(secs)}{note}")
            return
        if key == "enabled":
            if val.lower() in ("on", "true", "yes"):
                cfg["enabled"] = True
            elif val.lower() in ("off", "false", "no"):
                cfg["enabled"] = False
            else:
                print("enabled takes on|off")
                sys.exit(2)
            save_config(cfg)
            print(f"monitor {'enabled' if cfg['enabled'] else 'PAUSED'}")
            return
    print("usage: companion config [set interval <val> | set enabled on|off]")
    sys.exit(2)


def cmd_check():
    env = dict(os.environ, COMPANION_FORCE="1")
    out = subprocess.run([sys.executable, DETECT], capture_output=True,
                         text=True, env=env, timeout=120)
    line = out.stdout.strip().splitlines()[-1] if out.stdout.strip() else ""
    try:
        d = json.loads(line)
    except Exception:
        print("detector failed:", out.stdout[-300:], out.stderr[-300:])
        sys.exit(1)
    print(f"decision: {d['decision']} — {d['reason']}")
    for c in d.get("payload", {}).get("changes", []):
        print(f"  - {c}")


def cmd_ask(args):
    out = subprocess.run([sys.executable, ASK] + args,
                         capture_output=True, text=True, timeout=60)
    if out.returncode != 0:
        print("ask failed:", out.stderr[-300:])
        sys.exit(1)
    sys.stdout.write(out.stdout)


def cmd_gadget(args):
    out = subprocess.run([sys.executable, GADGET] + args,
                         capture_output=True, text=True, timeout=120)
    if out.returncode != 0:
        print("gadget failed:", out.stderr[-300:])
        sys.exit(1)
    sys.stdout.write(out.stdout)


def cmd_tracker(args):
    out = subprocess.run([sys.executable, TRACKER] + args,
                         capture_output=True, text=True, timeout=60)
    if out.returncode != 0:
        print("tracker failed:", out.stderr[-300:])
        sys.exit(1)
    sys.stdout.write(out.stdout)


def cmd_log(args):
    n = int(args[0]) if args else 10
    try:
        with open(HOOK_LOG) as f:
            lines = f.readlines()[-n:]
    except Exception:
        print("no hook log yet")
        return
    for line in lines:
        try:
            e = json.loads(line)
            ts = time.strftime("%H:%M:%S", time.localtime(e.get("finished_at_ms", 0) / 1000))
            print(f"{ts} {e.get('decision', '?')}: {e.get('entry', {}).get('reason', '')}")
        except Exception:
            continue


def main():
    args = sys.argv[1:]
    if not args or args[0] in ("-h", "--help", "help"):
        print(__doc__)
        return 0
    cmd, rest = args[0], args[1:]
    if cmd == "status":
        cmd_status()
    elif cmd == "config":
        cmd_config(rest)
    elif cmd == "check":
        cmd_check()
    elif cmd == "gadget":
        cmd_gadget(rest)
    elif cmd == "tracker":
        cmd_tracker(rest)
    elif cmd == "ask":
        cmd_ask(rest)
    elif cmd == "log":
        cmd_log(rest)
    else:
        print(f"unknown command: {cmd}\n{__doc__}")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
