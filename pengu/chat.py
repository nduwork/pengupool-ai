"""M3 chat: Claude <-> Muse messages along the team tree, recorded.

`pengu send --from <sid|owner> --to <sid|owner> --text ...` appends one line to
`chat.jsonl` (index + content) and nudges a live TUI pane with tmux send-keys
(literal text, no Enter). `pengu inbox <sid>` lists entries addressed to a
session; the agent answers with another `pengu send`.

Measured: `muse session-message` from outside a session is unavailable on this
machine (external agent ingress closed), so delivery is harness-side: the
inbox file plus a pane nudge. Either endpoint may be `owner`: owner-bound
entries sit in the owner's inbox (`pengu inbox owner`); the judge (M4) routes
them to a question or a proposal, it never happens on send.
"""

import json
import sys
import time
import uuid

from . import session

def _chat_path():
    home = session.home()
    home.mkdir(parents=True, exist_ok=True)
    return home / "chat.jsonl"


def _valid(e):
    return (isinstance(e, dict) and isinstance(e.get("id"), str)
            and isinstance(e.get("from"), str) and isinstance(e.get("to"), str)
            and isinstance(e.get("text"), str))


def _check_party(p):
    if p == "owner":
        return
    if not any(r["id"] == p for r in session.load()):
        raise ValueError(f"off the tree: no session {p}")


CHAT_KEEP = 30 * 86400


def send(from_sid, to_sid, text):
    """Record a turn; nudge a live pane. Returns the entry.

    `from_sid` is asserted, not authenticated: same-user local IPC cannot tell
    which session really called, so a session could send as another session.
    `from_sid="owner"` additionally needs a real terminal (a pty can fake
    `isatty`, so agents must never send as owner — the owner types at a
    terminal, speaks, or taps in the app).
    """
    if from_sid == "owner" and not sys.stdin.isatty():
        raise ValueError("only the owner, at a real terminal, sends as owner")
    if not isinstance(text, str) or not text.strip():
        raise ValueError("empty message")
    rows = {r["id"]: r for r in session.load()}  # one load; membership below
    for party in (from_sid, to_sid):
        if party != "owner" and party not in rows:
            raise ValueError(f"off the tree: no session {party}")
    now = time.time()
    entry = {"id": uuid.uuid4().hex[:8], "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
             "ts": now, "from": from_sid, "to": to_sid, "text": text}
    with session._locked():
        p = _chat_path()
        kept = []
        if p.exists():
            cutoff = now - CHAT_KEEP
            for raw in p.read_text(encoding="utf-8").splitlines():
                if not raw.strip():
                    continue
                try:
                    e = json.loads(raw)
                except ValueError:
                    continue
                if (_valid(e) and isinstance(e.get("ts"), (int, float))
                        and e["ts"] >= cutoff):
                    kept.append(raw)
        kept.append(json.dumps(entry))
        p.write_text("\n".join(kept) + "\n", encoding="utf-8")
    if to_sid != "owner":
        name = rows[to_sid]["tmux"]
        if session._tmux_alive(name):
            nudge = f"[pengu] message from {from_sid}; run: pengu inbox {to_sid}"
            try:
                session._tmux("send-keys", "-t", name, "-l", nudge)
            except RuntimeError:
                pass  # the line is recorded; the nudge is best-effort
    return entry


def inbox(sid):
    """All entries addressed to a session (or owner-bound), oldest first."""
    _check_party(sid)
    with session._locked():
        p = _chat_path()
        if not p.exists():
            return []
        raw_lines = p.read_text(encoding="utf-8").splitlines()
    out = []
    for raw in raw_lines:
        if not raw.strip():
            continue
        try:
            e = json.loads(raw)
        except ValueError:
            continue
        if _valid(e) and e.get("to") == sid:
            out.append(e)
    return out
