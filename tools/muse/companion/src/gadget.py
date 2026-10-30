#!/usr/bin/env python3
"""gadget — render the PenguPool team status card as chat-widget HTML.

Reads the live team through the paired-device API (pp.py): /sessions
gives each session's status, whether an ask waits on the owner, and its
team, role and parent.

The status indicators mirror PenguPool's own web UI (web/src/app/ctx.tsx
STATUS, colours from web/src/index.css design tokens):

  working   "Active"       green, pulsing   (success #15803d / #4ade80)
  idle      "Waiting"      amber            (warning #b45309 / #fbbf24)
  approval  "? Need input" red              (danger  #c0262d / #f87171)
  starting  "Starting"     grey, pulsing
  ended                    grey
  lost / unreachable       red

A running session with an ask waiting on the owner shows as approval.

Usage: gadget.py [--out PATH]   (HTML goes to stdout, or to PATH)
"""
import html
import json
import os
import sys
import time
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pp  # noqa: E402  (the device-API client)

STATUS = {
    "working": ("Active", "active"),
    "idle": ("Waiting", "waiting"),
    "approval": ("? Need input", "need"),
    "starting": ("Starting", "start"),
    "ended": ("Ended", "stale"),
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
.shd{padding:7px 12px 3px;font-size:11px;font-weight:600;letter-spacing:.04em;color:var(--hatch-widget-muted);text-transform:uppercase}
.ft{padding:8px 12px;font-size:11px;color:var(--hatch-widget-muted);border-top:1px solid var(--hatch-widget-border);line-height:1.5}
.chain{font-size:12px;padding-top:1px;padding-bottom:7px;color:var(--hatch-widget-muted)}
.csym{font-size:11px;margin-right:3px}
.cs-done{color:var(--pp-success)}
.cs-current{color:var(--pp-success);animation:pl 1.6s ease-in-out infinite}
.cs-todo{color:var(--pp-faint)}
.cs-blocked{color:var(--pp-danger)}
.cstep-done{color:var(--hatch-widget-muted)}
.cstep-current{color:var(--hatch-widget-text);font-weight:600}
.carr{color:var(--pp-faint);margin:0 3px}
"""

JS = """
(function(){
  var card=document.getElementById('ppcard');
  var state=__SEED__;
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
  try{ state=window.hatchWidget.getState(__SEED__)||state; }catch(e){}
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
    code, res = pp.api("GET", "/sessions")
    if code != 200:
        raise SystemExit(f"/sessions answered {code}: {res}")
    return res or []


def shown_key(s):
    """The status key PenguPool's UI would show for this session."""
    if s.get("state") != "running":
        return "ended"
    if s.get("waiting"):
        return "approval"
    st = s.get("status") or "idle"
    return st if st in STATUS else "idle"


def row_html(s, depth, anc, slug, has_kids):
    label, tone = STATUS[shown_key(s)]
    pad = 12 + depth * 20
    cls = "row foldable" if has_kids else "row"
    toggle = f' data-toggle="{html.escape(slug)}"' if has_kids else ""
    chev = CHEV if has_kids else ""
    tag = f' <span class="tag">{html.escape(s["role"].title())}</span>' if s.get("role") else ""
    return (f'<div class="{cls}" data-anc="{html.escape(anc)}"{toggle} style="padding-left:{pad}px">'
            f'{chev}<span class="dot d-{tone}"></span>'
            f'<span class="nm">{html.escape(s.get("name") or s.get("title", "?"))}</span>{tag}'
            f'<span class="st s-{tone}">{label}</span></div>')


def render():
    sessions = fetch()
    by_id = {s["session"]: s for s in sessions}

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
    out.append('<div class="hd"><div class="repo">PenguPool</div>')
    out.append(f'<div class="mon"><span class="dot d-active"></span>'
               f'companion checking every {interval}{last_txt}</div></div>')

    # Per-agent workflow chains (Muse's tracker, tracker.py) render
    # directly under each agent's name: A → B → C with a status symbol
    # per step. There is no separate tracker panel.
    wfmod = None
    wf = {"agents": {}}
    try:
        import tracker as wfmod
        wf = wfmod.load()
    except Exception:
        wfmod = None

    placed = set()
    groups = []
    teams = {}
    for s in sessions:
        if s.get("team_id"):
            teams.setdefault(s["team_id"], []).append(s)
    for tid, members in teams.items():
        ids = {m["session"] for m in members}
        main = next((m for m in members if m.get("role") == "LEAD"), members[0])
        kids, roots = {}, []
        for m in members:
            parent = m.get("parent")
            if parent not in ids:
                parent = "" if m is main else main["session"]
            if parent == "":
                roots.append(m["session"])
            else:
                kids.setdefault(parent, []).append(m["session"])
        rows = []

        def walk(sid, depth):
            if sid in placed:
                return
            placed.add(sid)
            rows.append((by_id[sid], depth, kids.get(sid, [])))
            for ch in kids.get(sid, []):
                walk(ch, depth + 1)

        for r in roots:
            walk(r, 0)
        groups.append((members[0].get("team") or "team", rows, ids))

    for gi, (tname, rows, ids) in enumerate(groups):
        tslug = f"team{gi}"
        out.append(f'<button type="button" class="ghd" data-toggle="{tslug}">'
                   f'{CHEV}{html.escape(tname)}'
                   f'<span class="cnt">{len(rows)} agents</span></button>')
        for s, depth, kid_ids in rows:
            slug = "n" + s["session"][-8:]
            # data-anc: the slugs of every ancestor (team + parent chain),
            # so folding any ancestor hides this row.
            chain = [tslug]
            cur, seen = s, set()
            while cur.get("parent") in ids and cur["parent"] not in seen:
                seen.add(cur["parent"])
                chain.append("n" + cur["parent"][-8:])
                cur = by_id[cur["parent"]]
            out.append(row_html(s, depth, " ".join(chain), slug, bool(kid_ids)))
            if wfmod:
                crow = wfmod.chain_row(wf, s.get("name") or s.get("title"),
                                       " ".join(chain) + " " + slug,
                                       12 + depth * 20 + 24)
                if crow:
                    out.append(crow)

    rest = [s for s in sessions if s["session"] not in placed]
    if rest:
        out.append('<div class="shd">Ungrouped</div>')
        for s in rest:
            out.append(row_html(s, 0, "", "", False))
            if wfmod:
                crow = wfmod.chain_row(wf, s.get("name") or s.get("title"), "", 36)
                if crow:
                    out.append(crow)

    default_closed = []
    now = datetime.now().astimezone().strftime("%-I:%M %p")
    out.append(f'<div class="ft">PenguPool\'s own indicators: green pulsing = Active, '
               f'amber = Waiting, red = ? Need input, grey = Ended. Tap a parent row to '
               f'fold or unfold its branch. Under each agent, its workflow: '
               f'✓ done · ● current · ○ todo. Rendered {now} by companion gadget.</div>')
    out.append("</div>")
    out.append("<script>" + JS.replace("__SEED__",
                                        json.dumps({"closed": default_closed})) + "</script>")
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
