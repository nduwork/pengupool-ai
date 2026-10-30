#!/usr/bin/env python3
"""gadget — render the PenguPool team status card as chat-widget HTML.

Reads the live team through the owner /ws API (pp.py): session.list for
status, a state snapshot for the team tree (members carry parent links),
settings.get for the daemon version, gates.open for check-ins.

The status indicators mirror PenguPool's own web UI (web/src/app/ctx.tsx
STATUS + shownStatus, colours from web/src/index.css design tokens):

  working   "Active"       green, pulsing   (success #15803d / #4ade80)
  idle      "Waiting"      amber            (warning #b45309 / #fbbf24)
  approval  "? Need input" red              (danger  #c0262d / #f87171)
  stale     "Stale"        grey             (faint   #8a91a0 / #6b7280)
  starting  "Starting"     grey, pulsing
  ended / exited           grey
  lost / unreachable       red

shown-status rules, same as the app: a running session whose status is
not "working" and which has an open check-in gate shows as approval; an
idle non-helper inactive for over 10 minutes shows as Stale (helpers
never go stale — waiting for their coordinator is their job). Context %
is coloured green below 30, amber below 60, red from 60 (ctxClass).

Usage: gadget.py [--out PATH]   (HTML goes to stdout, or to PATH)
"""
import html
import json
import os
import sys
import time
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pp  # noqa: E402  (the /ws owner-API client)

STALE_MS = 10 * 60 * 1000
STATUS = {
    "working": ("Active", "active"),
    "idle": ("Waiting", "waiting"),
    "approval": ("? Need input", "need"),
    "starting": ("Starting", "start"),
    "stale": ("Stale", "stale"),
    "terminal": ("Not an agent", "waiting"),
    "ended": ("Ended", "stale"),
    "exited": ("Exited", "stale"),
    "lost": ("Lost", "need"),
    "unreachable": ("Unreachable", "need"),
}
CONFIG_PATH = os.path.expanduser("~/workspace/companion/config.json")
STATE_PATH = os.path.expanduser("~/hooks/state/pengupool-status.json")

CSS = """
*{box-sizing:border-box}
.card{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;background:var(--hatch-widget-surface);border:1px solid var(--hatch-widget-border);border-radius:10px;overflow:hidden;color:var(--hatch-widget-text);
--pp-success:#15803d;--pp-warning:#b45309;--pp-danger:#c0262d;--pp-faint:#8a91a0}
html[data-theme="dark"] .card{--pp-success:#4ade80;--pp-warning:#fbbf24;--pp-danger:#f87171;--pp-faint:#6b7280}
.hd{display:flex;align-items:center;gap:8px;flex-wrap:wrap;padding:10px 12px;border-bottom:1px solid var(--hatch-widget-border)}
.repo{font-weight:600;font-size:14px}.repo span{color:var(--hatch-widget-muted);font-weight:400}
.badge{font-size:11px;padding:1px 7px;border:1px solid var(--hatch-widget-border);border-radius:999px;color:var(--hatch-widget-muted)}
.mon{margin-left:auto;font-size:11px;color:var(--hatch-widget-muted);display:flex;align-items:center;gap:6px}
.ghd{display:flex;align-items:center;gap:7px;width:100%;padding:9px 12px;background:var(--hatch-widget-surface-muted);border:none;border-bottom:1px solid var(--hatch-widget-border);color:var(--hatch-widget-text);font:inherit;font-size:13px;font-weight:600;text-align:left;cursor:pointer}
.ghd .cnt{margin-left:auto;font-size:11px;font-weight:400;color:var(--hatch-widget-muted)}
.chev{width:14px;height:14px;flex:none;color:var(--hatch-widget-muted);transition:transform .15s}
.row{display:flex;align-items:center;gap:8px;padding:8px 12px;border-bottom:1px solid var(--hatch-widget-border);font-size:13px}
.row:last-child{border-bottom:none}
.foldable{cursor:pointer}
.foldable:active{background:var(--hatch-widget-surface-muted)}
.dot{width:8px;height:8px;border-radius:50%;flex:none}
.d-waiting{background:var(--pp-warning)}.d-active{background:var(--pp-success);animation:pl 1.6s ease-in-out infinite}.d-need{background:var(--pp-danger)}.d-stale{background:var(--pp-faint)}.d-start{background:var(--pp-faint);animation:pl 1.6s ease-in-out infinite}
@keyframes pl{50%{opacity:.35}}
.nm{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.st{font-size:12px;white-space:nowrap}
.s-waiting{color:var(--pp-warning)}.s-active{color:var(--pp-success)}.s-need{color:var(--pp-danger);font-weight:600}.s-stale{color:var(--pp-faint)}
.tag{font-size:10px;padding:0 5px;border:1px solid var(--hatch-widget-border);border-radius:4px;color:var(--hatch-widget-muted)}
.right{margin-left:auto;display:flex;gap:10px;align-items:center;flex:none;font-size:11px;color:var(--hatch-widget-muted)}
.ctx-ok{color:var(--pp-success);font-weight:600}.ctx-warn{color:var(--pp-warning);font-weight:600}.ctx-bad{color:var(--pp-danger);font-weight:600}
.shd{padding:7px 12px 3px;font-size:11px;font-weight:600;letter-spacing:.04em;color:var(--hatch-widget-muted);text-transform:uppercase}
.ft{padding:8px 12px;font-size:11px;color:var(--hatch-widget-muted);border-top:1px solid var(--hatch-widget-border);line-height:1.5}
"""

JS = """
(function(){
  var card=document.getElementById('ppcard');
  var state={closed:[]};
  function apply(){
    var closed={}; state.closed.forEach(function(id){closed[id]=true;});
    card.querySelectorAll('[data-anc]').forEach(function(r){
      var anc=r.getAttribute('data-anc').split(' ');
      r.style.display=anc.some(function(a){return closed[a];})?'none':'flex';
    });
    card.querySelectorAll('[data-toggle]').forEach(function(el){
      var id=el.getAttribute('data-toggle');
      var svg=el.querySelector('.chev');
      if(svg) svg.style.transform=closed[id]?'rotate(-90deg)':'';
    });
  }
  function save(){ if(window.hatchWidget) window.hatchWidget.setState(state); }
  try{ state=window.hatchWidget.getState({closed:[]})||state; }catch(e){}
  apply();
  card.addEventListener('click',function(e){
    var el=e.target.closest('[data-toggle]'); if(!el) return;
    var id=el.getAttribute('data-toggle'); var i=state.closed.indexOf(id);
    if(i>=0) state.closed.splice(i,1); else state.closed.push(id);
    save(); apply();
  });
  window.addEventListener('hatch-widget-state',function(e){ if(e.detail&&e.detail.state){ state=e.detail.state; apply(); } });
})();
"""

CHEV = ('<svg class="chev" viewBox="0 0 16 16" fill="currentColor">'
        '<path d="M4 6l4 4 4-4z"/></svg>')


def parse_ms(ts):
    if not ts:
        return None
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp() * 1000
    except Exception:
        return None


def ago(ts):
    """PenguPool's ago(): 5s ago / 3m ago / 2h ago / 1d ago."""
    ms = parse_ms(ts)
    if ms is None:
        return ""
    s = max(0, (time.time() * 1000 - ms) / 1000)
    if s < 60:
        return f"{int(s)}s ago"
    if s < 3600:
        return f"{int(s // 60)}m ago"
    if s < 86400:
        return f"{int(s // 3600)}h ago"
    return f"{int(s // 86400)}d ago"


def fmt_interval(secs):
    if secs % 3600 == 0:
        return f"{secs // 3600}h"
    if secs % 60 == 0:
        return f"{secs // 60}m"
    return f"{secs}s"


def fetch():
    c = pp.Client()
    try:
        c.hello()
        sessions = c.call("session.list").get("sessions", [])
        res = c.call("settings.get")
        version = (res.get("result", res) or {}).get("version")
        mid = c.send_control({"type": "state.subscribe"})
        domain, snap_sessions = {}, []
        deadline = time.time() + 20
        while time.time() < deadline:
            ftype, _ch, payload = c.read_frame(timeout=20)
            if ftype != pp.FT_CONTROL:
                continue
            m = json.loads(payload)
            if m.get("type") == "state.snapshot":
                domain = m.get("domain", {})
                snap_sessions = m.get("sessions", [])
                break
        checkins = set()
        try:
            g = c.call("gates.open")
            gres = g.get("result", g)
            gates = gres if isinstance(gres, list) else gres.get("gates", [])
            checkins = {x["session_id"] for x in gates if x.get("kind") == "checkin"}
        except Exception:
            pass
        return sessions, domain, version, checkins
    finally:
        c.close()


def shown_key(s, member, checkins):
    """The status key PenguPool's UI would show for this session."""
    if s.get("state") != "running":
        return s.get("state") if s.get("state") in STATUS else "ended"
    st = s.get("status") or "idle"
    if st == "idle" and (member is None or member.get("role") != "helper"):
        ms = parse_ms(s.get("active_at"))
        if ms is not None and time.time() * 1000 - ms > STALE_MS:
            st = "stale"
    if st not in ("working", "lost", "unreachable") and s["id"] in checkins:
        return "approval"
    return st if st in STATUS else "idle"


def row_html(s, member, checkins, depth, anc, slug, has_kids):
    key = shown_key(s, member, checkins)
    label, tone = STATUS[key]
    pad = 12 + depth * 20
    cls = "row foldable" if has_kids else "row"
    toggle = f' data-toggle="{slug}"' if has_kids else ""
    chev = CHEV if has_kids else ""
    tag = ' <span class="tag">Lead</span>' if member and member.get("role") == "main" else ""
    right = ""
    pct = s.get("context_pct")
    if pct is not None:
        cc = "ctx-ok" if pct < 30 else "ctx-warn" if pct < 60 else "ctx-bad"
        right += f'<span class="{cc}">{pct}%</span>'
    a = ago(s.get("active_at"))
    if a:
        right += f"<span>{a}</span>"
    return (f'<div class="{cls}" data-anc="{anc}"{toggle} style="padding-left:{pad}px">'
            f'{chev}<span class="dot d-{tone}"></span>'
            f'<span class="nm">{html.escape(s.get("name", "?"))}</span>{tag}'
            f'<span class="st s-{tone}">{label}</span>'
            f'<span class="right">{right}</span></div>')


def render():
    sessions, domain, version, checkins = fetch()
    by_id = {s["id"]: s for s in sessions}
    agents = [s for s in sessions if s.get("harness") != "shell"]
    shells = [s for s in sessions if s.get("harness") == "shell"]

    cfg = {}
    try:
        with open(CONFIG_PATH) as f:
            cfg = json.load(f)
    except Exception:
        pass
    interval = fmt_interval(int(cfg.get("interval_secs", 60)))
    last_txt = ""
    try:
        with open(STATE_PATH) as f:
            st = json.load(f)
        if st.get("last_check_epoch"):
            last_txt = f" · last check {ago(datetime.fromtimestamp(st['last_check_epoch'], timezone.utc).isoformat())}"
    except Exception:
        pass

    out = [f"<style>{CSS}</style>", '<div class="card" id="ppcard">']
    out.append('<div class="hd"><div class="repo"><span>nduworker /</span> pengupool-ai</div>')
    if version:
        out.append(f'<div class="badge">v{html.escape(str(version))}</div>')
    out.append(f'<div class="mon"><span class="dot d-active"></span>'
               f'companion checking every {interval}{last_txt}</div></div>')

    placed = set()
    groups = []
    for t in domain.get("teams", []):
        if t.get("state") != "active":
            continue
        members = [m for m in t.get("members", [])
                   if m.get("state") == "active" and m["session_id"] in by_id]
        if not members:
            continue
        ids = {m["session_id"] for m in members}
        main = next((m for m in members if m.get("role") == "main"), members[0])
        kids, roots = {}, []
        for m in members:
            parent = m.get("parent")
            if parent not in ids:
                parent = "" if m is main else main["session_id"]
            if parent == "":
                roots.append(m["session_id"])
            else:
                kids.setdefault(parent, []).append(m["session_id"])
        rows = []

        def walk(sid, depth):
            if sid in placed:
                return
            placed.add(sid)
            member = next(m for m in members if m["session_id"] == sid)
            rows.append((by_id[sid], member, depth, kids.get(sid, [])))
            for ch in kids.get(sid, []):
                walk(ch, depth + 1)

        for r in roots:
            walk(r, 0)
        groups.append((t, rows, members))

    for gi, (t, rows, members) in enumerate(groups):
        tslug = f"team{gi}"
        by_member = {m["session_id"]: m for m in members}
        out.append(f'<button type="button" class="ghd" data-toggle="{tslug}">'
                   f'{CHEV}{html.escape(t.get("name", "team"))}'
                   f'<span class="cnt">{len(rows)} agents</span></button>')
        for s, member, depth, kid_ids in rows:
            slug = "n" + s["id"][-8:]
            # data-anc: the slugs of every ancestor (team + parent chain),
            # so folding any ancestor hides this row.
            chain = [tslug]
            cur, seen = member, set()
            while cur.get("parent") in by_member and cur["parent"] not in seen:
                seen.add(cur["parent"])
                chain.append("n" + cur["parent"][-8:])
                cur = by_member[cur["parent"]]
            out.append(row_html(s, member, checkins, depth,
                                " ".join(chain), slug, bool(kid_ids)))

    rest = [s for s in agents if s["id"] not in placed]
    if rest:
        out.append('<div class="shd">Ungrouped</div>')
        for s in rest:
            out.append(row_html(s, None, checkins, 0, "", "", False))

    if shells:
        out.append('<div class="shd">Terminals</div>')
        for s in shells:
            cwd = (s.get("cwd") or "").rstrip("/").split("/")[-1] or "/"
            out.append(f'<div class="row" style="padding-left:16px">'
                       f'<span class="nm" style="font-weight:500">{html.escape(s.get("name", "terminal"))}</span>'
                       f'<span class="right"><span>{html.escape(cwd)}</span></span></div>')

    now = datetime.now().astimezone().strftime("%-I:%M %p")
    out.append(f'<div class="ft">PenguPool\'s own indicators: green pulsing = Active, '
               f'amber = Waiting, red = ? Need input, grey = Stale. Tap a parent row to '
               f'fold or unfold its branch. Rendered {now} by companion gadget.</div>')
    out.append("</div>")
    out.append(f"<script>{JS}</script>")
    return "\n".join(out)


def main():
    args = sys.argv[1:]
    out_path = None
    if "--out" in args:
        out_path = args[args.index("--out") + 1]
    text = render()
    if out_path:
        with open(out_path, "w") as f:
            f.write(text)
        print(out_path)
    else:
        print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
