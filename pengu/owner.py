"""M3 owner API: the Muse-app agent's window into questions and proposals.

Stdlib `ThreadingHTTPServer` on the owner's machine (localhost; tailnet ACL is
an owner action, see README). Auth: bearer token from `PENGUPOOL_OWNER_TOKEN`
(read from the macOS keychain at launch); the server refuses to start without
it. All endpoints need it.

- `GET /questions?wait=N` (long-poll) -> pending items
  `{id, session, asked_at, question, options, payload_hash, expires_at}`.
- `POST /questions/{id}/answer` `{option, payload_hash, idempotency_key}`.
  Only an offered option with a matching hash is accepted. Changed or expired
  -> 409, no effect. The same `idempotency_key` replays the first result.
  No answer leaves the session waiting: never a yes or a no.
- `POST /proposals` `{words}` -> 202, stored as a proposal; the owner confirms
  with a second tap or in the terminal.

Questions are created locally (`pengu ask` writes the state file, then polls
it); the network API only reads and answers. Text written by another agent is
untrusted data; the owner sees it as such.
"""

import hashlib
import hmac
import json
import math
import os
import sys
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

from . import session

DEFAULT_PORT = 8471


def _state_path():
    home = session.home()
    home.mkdir(parents=True, exist_ok=True)
    return home / "owner.json"


def _blank():
    return {"questions": {}, "idempotency": {}, "proposals": []}


def _quarantine(p):
    backup = p.with_suffix(f".corrupt-{int(time.time())}.json")
    try:
        p.rename(backup)
    except OSError:
        pass  # unreadable and unmovable: start blank, original stays
    return _blank()


def _load_state():
    p = _state_path()
    if not p.exists():
        return _blank()
    try:
        st = json.loads(p.read_text(encoding="utf-8"))
    except ValueError:
        return _quarantine(p)
    if (not isinstance(st, dict) or not isinstance(st.get("questions"), dict)
            or not isinstance(st.get("idempotency"), dict)
            or not isinstance(st.get("proposals"), list)):
        return _quarantine(p)
    st["questions"] = {k: q for k, q in st["questions"].items() if _valid_question(q)}
    st["idempotency"] = {k: r for k, r in st["idempotency"].items() if _valid_verdict(r)}
    st["proposals"] = [x for x in st["proposals"] if _valid_proposal(x)]
    return st


def _valid_proposal(x):
    return (isinstance(x, dict) and isinstance(x.get("words"), str)
            and isinstance(x.get("at"), (int, float)) and isinstance(x.get("status"), str))


def _valid_verdict(r):
    return (isinstance(r, dict) and isinstance(r.get("status"), int)
            and isinstance(r.get("body"), dict))


def _valid_question(q):
    if not (isinstance(q, dict) and isinstance(q.get("id"), str)
            and isinstance(q.get("status"), str)
            and isinstance(q.get("asked_at"), (int, float))
            and isinstance(q.get("expires_at"), (int, float))
            and isinstance(q.get("payload_hash"), str)):
        return False
    if not isinstance(q.get("options"), list) or not q["options"] \
            or any(not isinstance(o, str) for o in q["options"]):
        return False
    a = q.get("answer")
    if q["status"] == "answered":
        return isinstance(a, dict) and isinstance(a.get("option"), str)
    return a is None


def _load_swept():
    """Load + expire + prune + save. Caller holds the lock."""
    st = _load_state()
    _sweep(st, time.time())  # saves itself when it changes anything
    return st


def _save_state(st):
    _state_path().write_text(json.dumps(st), encoding="utf-8")


def _payload(session_id, question, options):
    canon = json.dumps({"s": session_id, "q": question, "o": options}, sort_keys=True)
    return hashlib.sha256(canon.encode()).hexdigest()[:16]


def create_question(session_id, question, options, expires_in=300):
    """Create a pending question; returns it. Local only (no HTTP needed)."""
    if not isinstance(question, str) or not question.strip():
        raise ValueError("empty question")
    if not isinstance(options, list) or not 2 <= len(options) <= 3:
        raise ValueError("give 2 or 3 options")
    if any(not isinstance(o, str) or not o.strip() for o in options):
        raise ValueError("options are non-empty strings")
    if len(set(options)) != len(options):
        raise ValueError("options are unique")
    if session_id == "owner" or not any(r["id"] == session_id for r in session.load()):
        raise ValueError(f"off the tree: no session {session_id}")
    now = time.time()
    q = {"id": uuid.uuid4().hex[:12], "session": session_id, "asked_at": now,
         "question": question, "options": list(options),
         "payload_hash": _payload(session_id, question, list(options)),
         "expires_at": now + max(1, expires_in), "status": "pending", "answer": None}
    with session._locked():
        st = _load_swept()
        st["questions"][q["id"]] = q
        _save_state(st)
    return q


PRUNE_AFTER = 7 * 86400


def _sweep(st, now):
    """Expire stale pendings; drop settled rows older than PRUNE_AFTER. Returns changed."""
    changed = False
    for q in st["questions"].values():
        if q["status"] == "pending" and now > q["expires_at"]:
            q["status"] = "expired"
            changed = True
    old_q = [qid for qid, q in st["questions"].items()
             if q["status"] != "pending" and now - q.get("asked_at", now) > PRUNE_AFTER]
    for qid in old_q:
        del st["questions"][qid]
        changed = True
    old_k = [k for k, r in st["idempotency"].items()
             if now - r.get("at", now) > PRUNE_AFTER]
    for k in old_k:
        del st["idempotency"][k]
        changed = True
    old_p, fresh_p = [], []
    for p in st["proposals"]:
        (old_p if now - p.get("at", now) > 30 * 86400 else fresh_p).append(p)
    if old_p:
        st["proposals"] = fresh_p
        changed = True
    if changed:
        _save_state(st)
    return changed


def pending_questions():
    with session._locked():
        st = _load_swept()  # sweeps, expires, prunes
        return [q for q in st["questions"].values() if q["status"] == "pending"]


def answer_question(qid, option, payload_hash, idempotency_key):
    """Apply an owner's answer. Returns (http_status, body).

    Every keyed outcome is stored per question: the same idempotency_key on
    the same question replays the first result, success or error."""
    if not idempotency_key or not isinstance(idempotency_key, str):
        return 400, {"error": "idempotency_key required"}
    key = qid + "\x00" + idempotency_key
    with session._locked():
        st = _load_swept()
        if key in st["idempotency"]:
            r = st["idempotency"][key]
            return r["status"], r["body"]

        def _decide(status, body):
            st["idempotency"][key] = {"status": status, "body": body, "at": time.time()}
            _save_state(st)
            return status, body

        q = st["questions"].get(qid)
        now = time.time()
        if q is None:
            return _decide(404, {"error": "no such question"})
        if q["status"] == "answered":
            return _decide(409, {"error": "already answered"})
        if q["status"] != "pending" or now > q["expires_at"]:
            q["status"] = "expired"
            return _decide(409, {"error": "expired"})
        if payload_hash != q["payload_hash"]:
            return _decide(409, {"error": "changed"})
        if option not in q["options"]:
            return _decide(400, {"error": "not an offered option"})
        q["status"] = "answered"
        q["answer"] = {"option": option, "at": now}
        return _decide(200, {"id": qid, "option": option, "answered_at": now})


def save_proposal(words):
    if not isinstance(words, str) or not words.strip():
        return 400, {"error": "empty proposal"}
    with session._locked():
        st = _load_swept()
        st["proposals"].append({"at": time.time(), "words": words, "status": "proposed"})
        _save_state(st)
    return 202, {"status": "proposed"}


def list_proposals():
    with session._locked():
        return _load_swept()["proposals"]


def wait_answer(qid, timeout):
    """Poll until answered. Returns (option, None), (None, 'expired') when the
    question died, or (None, 'timeout') when it is still pending."""
    end = time.time() + max(0, timeout)
    while time.time() < end:
        with session._locked():
            st = _load_swept()
            q = st["questions"].get(qid)
            if q is None:
                return None, "no such question"
            if q["status"] == "answered":
                return q["answer"]["option"], None
            if q["status"] == "expired" or time.time() > q["expires_at"]:
                return None, "expired"
        time.sleep(0.5)
    with session._locked():
        q = _load_swept()["questions"].get(qid)
    if q is None:
        return None, "no such question"
    if q["status"] == "answered":
        return q["answer"]["option"], None
    if q["status"] == "expired" or time.time() > q["expires_at"]:
        return None, "expired"
    return None, "timeout"


def make_handler(token):
    class Handler(BaseHTTPRequestHandler):
        def _send(self, code, body):
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def _authed(self):
            try:
                return hmac.compare_digest(self.headers.get("Authorization", ""),
                                           "Bearer " + token)
            except TypeError:
                return False  # non-ASCII header: never authentic

        def _body(self):
            try:
                n = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                return None
            if n < 0 or n > 1 << 20:
                return None  # 1 MiB cap; bigger is not our protocol
            if n == 0:
                return {}
            try:
                return json.loads(self.rfile.read(n))
            except ValueError:
                return None

        def do_GET(self):
            if not self._authed():
                return self._send(401, {"error": "unauthorized"})
            u = urlparse(self.path)
            if u.path == "/proposals":
                return self._send(200, list_proposals())
            if u.path != "/questions":
                return self._send(404, {"error": "unknown path"})
            try:
                wait = float((parse_qs(u.query).get("wait") or ["0"])[0])
            except ValueError:
                return self._send(400, {"error": "bad wait"})
            if not math.isfinite(wait):
                return self._send(400, {"error": "bad wait"})
            wait = max(0.0, min(120.0, wait))
            end = time.time() + wait
            items = pending_questions()
            while not items and time.time() < end:
                time.sleep(0.25)
                items = pending_questions()
            return self._send(200, items)

        def do_POST(self):
            if not self._authed():
                return self._send(401, {"error": "unauthorized"})
            u = urlparse(self.path)
            body = self._body()
            if not isinstance(body, dict):
                return self._send(400, {"error": "bad json"})
            parts = u.path.strip("/").split("/")
            if len(parts) == 3 and parts[0] == "questions" and parts[2] == "answer":
                code, out = answer_question(parts[1], body.get("option"),
                                            body.get("payload_hash"),
                                            body.get("idempotency_key"))
                return self._send(code, out)
            if u.path == "/proposals":
                code, out = save_proposal(body.get("words", ""))
                return self._send(code, out)
            return self._send(404, {"error": "unknown path"})

        def log_message(self, *a):
            pass

    return Handler


def serve(bind="127.0.0.1", port=DEFAULT_PORT):
    token = os.environ.get("PENGUPOOL_OWNER_TOKEN", "")
    if not token:
        raise RuntimeError("set PENGUPOOL_OWNER_TOKEN first (from the keychain)")
    if bind not in ("127.0.0.1", "::1", "localhost"):
        if os.environ.get("PENGUPOOL_OWNER_PUBLIC") != "1":
            raise RuntimeError("refusing a non-loopback bind without PENGUPOOL_OWNER_PUBLIC=1: "
                               "the bearer token travels in cleartext HTTP")
        print("warning: bearer auth over cleartext HTTP off localhost; "
              "use a Tailscale ACL (see README)", file=sys.stderr)
    srv = ThreadingHTTPServer((bind, port), make_handler(token))
    srv.daemon_threads = True
    return srv


def main_serve(bind="127.0.0.1", port=DEFAULT_PORT):  # pragma: no cover - manual run
    srv = serve(bind, port)
    print(f"owner API on {bind}:{port} (localhost; tailnet ACL is an owner action)")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
