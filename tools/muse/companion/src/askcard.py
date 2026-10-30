"""Context cards: render a pending agent question for the owner's phone.

Per ANSWER-CARDS.md: this HTML card carries the context only (agent,
question, background). Choices live on the native choice card, whose
tap files the answer immediately; open questions take their answer in
the conversation. Optionally renders the closed state after an answer
has been filed.
"""
import html

CSS = """
*{box-sizing:border-box}
.card{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;background:var(--hatch-widget-surface);border:1px solid var(--hatch-widget-border);border-radius:12px;overflow:hidden;color:var(--hatch-widget-text);
--pp-success:#15803d;--pp-warning:#b45309;--pp-danger:#c0262d;--pp-accent:#2563eb;--pp-faint:#8a91a0}
html[data-theme="dark"] .card{--pp-success:#4ade80;--pp-warning:#fbbf24;--pp-danger:#f87171;--pp-accent:#60a5fa;--pp-faint:#6b7280}
.hd{display:flex;align-items:center;gap:8px;padding:11px 14px;border-bottom:1px solid var(--hatch-widget-border)}
.dot{width:9px;height:9px;border-radius:50%;flex:none;background:var(--pp-danger)}
.dot.ok{background:var(--pp-success)}
.who{font-size:13px;color:var(--hatch-widget-muted)}
.who b{color:var(--hatch-widget-text);font-weight:600}
.chip{margin-left:auto;font-size:11px;font-weight:600;color:var(--pp-danger);border:1px solid var(--pp-danger);border-radius:999px;padding:2px 8px;white-space:nowrap}
.chip.ok{color:var(--pp-success);border-color:var(--pp-success)}
.q{padding:13px 14px 4px;font-size:16.5px;font-weight:650;line-height:1.35}
.body{padding:4px 14px 2px;font-size:13.5px;line-height:1.6}
.ctx{margin:10px 14px;padding:10px 12px;background:var(--hatch-widget-surface-muted);border-radius:9px;font-size:13px;line-height:1.55}
.ctx .lab{font-size:10.5px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--hatch-widget-muted);margin-bottom:4px}
.opts{padding:4px 8px 6px}
.opt{display:flex;gap:11px;align-items:flex-start;padding:12px 10px;margin-bottom:6px;border:1.5px solid var(--hatch-widget-border);border-radius:10px}
.radio{width:19px;height:19px;border-radius:50%;border:2px solid var(--hatch-widget-muted);flex:none;margin-top:1px;position:relative}
.ot{font-size:14.5px;font-weight:600;line-height:1.3}
.oc{font-size:12.5px;color:var(--hatch-widget-muted);line-height:1.45;margin-top:2px}
.opt.sel{border-color:var(--pp-accent);background:color-mix(in srgb,var(--pp-accent) 7%,transparent)}
.opt.sel .radio{border-color:var(--pp-accent)}
.opt.sel .radio::after{content:"";position:absolute;inset:3px;border-radius:50%;background:var(--pp-accent)}
.selbadge{margin-left:auto;font-size:10.5px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;color:var(--pp-accent);white-space:nowrap;padding-top:3px}
.ans{margin:10px 14px 13px;padding:11px 13px;background:var(--hatch-widget-surface-muted);border-left:3px solid var(--pp-success);border-radius:6px;font-size:14px;line-height:1.6}
.ft{padding:10px 14px 12px;font-size:12px;color:var(--hatch-widget-muted);line-height:1.55;border-top:1px solid var(--hatch-widget-border)}
"""


def esc(s):
    return html.escape(str(s if s is not None else ""))


def option_labels(spec):
    labels = [o["label"] for o in spec.get("options") or []]
    if spec.get("options") and spec.get("own_answer", True):
        labels.append("My own answer…")
    return labels


def _header(spec, closed=False):
    chip = '<span class="chip ok">Answered</span>' if closed else \
        '<span class="chip">? Needs your input</span>'
    dot = '<span class="dot ok"></span>' if closed else '<span class="dot"></span>'
    scope = (" · " + esc(spec["scope"])) if spec.get("scope") else ""
    return ('<div class="hd">%s<span class="who"><b>%s</b>%s</span>%s</div>'
            % (dot, esc(spec["agent"]), scope, chip))


def render_question(spec):
    parts = ["<style>" + CSS + "</style>", '<div class="card">', _header(spec)]
    parts.append('<div class="q">%s</div>' % esc(spec["title"]))
    if spec.get("body"):
        parts.append('<div class="body">%s</div>' % esc(spec["body"]))
    if spec.get("context"):
        parts.append('<div class="ctx"><div class="lab">Context</div>%s</div>'
                     % esc(spec["context"]))
    if spec.get("options"):
        parts.append('<div class="ft">The choices are on the card below — '
                     'tapping one files it right away, no extra confirmation.</div>')
    else:
        parts.append('<div class="ft">Answer here in the conversation — type or '
                     'dictate. If I draft the wording for you, you will see it and '
                     'confirm before anything is filed.</div>')
    parts.append('</div>')
    return "".join(parts)


def render_closed(spec, answer):
    parts = ["<style>" + CSS + "</style>", '<div class="card">', _header(spec, closed=True)]
    parts.append('<div class="q">%s</div>' % esc(spec["title"]))
    parts.append('<div class="ans">%s</div>' % esc(answer))
    parts.append('<div class="ft">Answer filed. This card is closed.</div>')
    parts.append('</div>')
    return "".join(parts)


if __name__ == "__main__":
    import json
    import sys
    from pathlib import Path
    spec = json.loads(Path(sys.argv[1]).read_text())
    out_dir = Path.home() / "workspace/companion/out"
    out_dir.mkdir(parents=True, exist_ok=True)
    if "--answer" in sys.argv:
        answer = sys.argv[sys.argv.index("--answer") + 1]
        page = render_closed(spec, answer)
        out = out_dir / ("ask-%s-closed.html" % spec["id"])
    else:
        page = render_question(spec)
        out = out_dir / ("ask-%s.html" % spec["id"])
    out.write_text(page)
    print(out)
    for label in option_labels(spec):
        print("OPTION:", label)
