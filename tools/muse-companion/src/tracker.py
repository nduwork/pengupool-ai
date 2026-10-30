#!/usr/bin/env python3
"""tracker — render the work-tracker card as chat-widget HTML.

The tracker is the owner's Muse's own record: epics and work-status
tracking deliberately live OUTSIDE PenguPool (the owner's rule), so this
snapshot card — curated by Muse, updated as work changes — is where the
tracker shows up. Live session status belongs to the mini site and the
status gadget; this card answers "what is being worked on, what is open,
what is paused, what is done".

Data: a JSON file (default ~/workspace/companion/tracker.json, override
with COMPANION_TRACKER or --file):

  {"title": "…", "sections": [{"name": "Open", "items": [
      {"title": "…", "note": "…", "state": "open|progress|blocked|paused|done"}]}]}

Usage: tracker.py [--file PATH] [--out PATH]
"""
import html
import json
import os
import sys
from datetime import datetime

CHIP = {
    "progress": ("In progress", "active"),
    "open": ("Open", "waiting"),
    "blocked": ("Blocked", "need"),
    "paused": ("Paused", "stale"),
    "done": ("Done", "active"),
}
CSS = """
*{box-sizing:border-box}
.card{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;background:var(--hatch-widget-surface);border:1px solid var(--hatch-widget-border);border-radius:10px;overflow:hidden;color:var(--hatch-widget-text);
--pp-success:#15803d;--pp-warning:#b45309;--pp-danger:#c0262d;--pp-faint:#8a91a0}
html[data-theme="dark"] .card{--pp-success:#4ade80;--pp-warning:#fbbf24;--pp-danger:#f87171;--pp-faint:#6b7280}
.hd{display:flex;align-items:center;gap:8px;flex-wrap:wrap;padding:10px 12px;border-bottom:1px solid var(--hatch-widget-border)}
.repo{font-weight:600;font-size:14px}
.badge{font-size:11px;padding:1px 7px;border:1px solid var(--hatch-widget-border);border-radius:999px;color:var(--hatch-widget-muted)}
.mon{margin-left:auto;font-size:11px;color:var(--hatch-widget-muted)}
.shd{padding:9px 12px 3px;font-size:11px;font-weight:600;letter-spacing:.04em;color:var(--hatch-widget-muted);text-transform:uppercase}
.row{display:flex;align-items:flex-start;gap:8px;padding:7px 12px;border-bottom:1px solid var(--hatch-widget-border);font-size:13px}
.row:last-child{border-bottom:none}
.dot{width:8px;height:8px;border-radius:50%;flex:none;margin-top:5px}
.d-waiting{background:var(--pp-warning)}.d-active{background:var(--pp-success)}.d-need{background:var(--pp-danger)}.d-stale{background:var(--pp-faint)}
.body{flex:1;min-width:0}
.nm{font-weight:600}
.note{font-size:12px;color:var(--hatch-widget-muted);line-height:1.45}
.chip{flex:none;font-size:11px;white-space:nowrap;padding-top:1px}
.c-waiting{color:var(--pp-warning)}.c-active{color:var(--pp-success)}.c-need{color:var(--pp-danger);font-weight:600}.c-stale{color:var(--pp-faint)}
.ft{padding:8px 12px;font-size:11px;color:var(--hatch-widget-muted);border-top:1px solid var(--hatch-widget-border);line-height:1.5}
"""


def render(data):
    out = [f"<style>{CSS}</style>", '<div class="card">']
    out.append('<div class="hd"><div class="repo">'
               f'{html.escape(data.get("title", "Work tracker"))}</div>')
    out.append('<div class="badge">tracked by Muse</div>')
    now = datetime.now().astimezone().strftime("%-I:%M %p")
    out.append(f'<div class="mon">snapshot {now}</div></div>')
    for sec in data.get("sections", []):
        items = sec.get("items", [])
        if not items:
            continue
        out.append(f'<div class="shd">{html.escape(sec.get("name", ""))} · {len(items)}</div>')
        for it in items:
            label, tone = CHIP.get(it.get("state", "open"), CHIP["open"])
            note = (f'<div class="note">{html.escape(it["note"])}</div>'
                    if it.get("note") else "")
            out.append(
                f'<div class="row"><span class="dot d-{tone}"></span>'
                f'<div class="body"><span class="nm">{html.escape(it.get("title", "?"))}</span>{note}</div>'
                f'<span class="chip c-{tone}">{label}</span></div>')
    out.append('<div class="ft">Epics and work tracking live here, with Muse — '
               'never inside PenguPool (the owner\'s rule). Live session status '
               'is on the mini site; this card is the tracker snapshot.</div>')
    out.append("</div>")
    return "\n".join(out)


def main():
    args = sys.argv[1:]
    path = os.environ.get("COMPANION_TRACKER",
                          os.path.expanduser("~/workspace/companion/tracker.json"))
    out_path = None
    if "--file" in args:
        path = args[args.index("--file") + 1]
    if "--out" in args:
        out_path = args[args.index("--out") + 1]
    with open(path) as f:
        data = json.load(f)
    text = render(data)
    if out_path:
        with open(out_path, "w") as f:
            f.write(text)
        print(out_path)
    else:
        print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
