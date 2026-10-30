#!/usr/bin/env python3
"""tracker — per-agent workflow chains for the status card.

Each agent's workflow is a short chain of steps, A → B → C, with a
symbol per step showing its status: ✓ done, ● current, ○ todo,
✕ blocked. The status card (gadget.py) renders the chain directly
under the agent's name; this module owns the data and the chain
markup. There is no separate tracker panel.

The data is Muse's own curation — work tracking lives outside
PenguPool (the owner's rule) — in ~/workspace/companion/tracker.json
(override with COMPANION_TRACKER or --file):

  {"agents": {"<session name>": {"steps": [
      {"label": "…", "status": "done|current|todo|blocked"}]}}}

Keep labels to two or three words so a chain fits one line, and keep
chains short: the steps the agent is actually moving through, nothing
more.

Usage: tracker.py [--file PATH]   (prints the chains as text)
"""
import html
import json
import os
import sys

SYMBOL = {"done": "✓", "current": "●", "todo": "○", "blocked": "✕"}

CHAIN_CSS = """
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


def load(path=None):
    path = path or os.environ.get(
        "COMPANION_TRACKER",
        os.path.expanduser("~/workspace/companion/tracker.json"))
    with open(path) as f:
        return json.load(f)


def steps_of(data, agent_name):
    return (data.get("agents", {}).get(agent_name) or {}).get("steps") or []


def chain_inner(steps):
    """The A → B → C spans for one agent's steps (no row wrapper)."""
    parts = []
    for i, s in enumerate(steps):
        st = s.get("status", "todo")
        if i:
            parts.append('<span class="carr">→</span>')
        parts.append(f'<span class="csym cs-{st}">{SYMBOL.get(st, "○")}</span>'
                     f'<span class="cstep-{st}">{html.escape(s.get("label", ""))}</span>')
    return "".join(parts)


def chain_row(data, agent_name, anc, pad):
    """A gadget-compatible row holding the agent's chain, or '' if none."""
    steps = steps_of(data, agent_name)
    if not steps:
        return ""
    return (f'<div class="row chain" data-anc="{anc}" style="padding-left:{pad}px">'
            f'<span>{chain_inner(steps)}</span></div>')


def text_lines(data):
    lines = []
    for name, wf in data.get("agents", {}).items():
        steps = (wf or {}).get("steps") or []
        if not steps:
            continue
        chain = " → ".join(f"{SYMBOL.get(s.get('status', 'todo'), '○')} {s.get('label', '')}"
                            for s in steps)
        lines.append(f"{name}: {chain}")
    return lines


def main():
    args = sys.argv[1:]
    path = None
    if "--file" in args:
        path = args[args.index("--file") + 1]
    lines = text_lines(load(path))
    print("\n".join(lines) if lines else "no agent workflows tracked")
    return 0


if __name__ == "__main__":
    sys.exit(main())
