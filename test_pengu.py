"""python3 test_pengu.py: the client against a local fake endpoint, the Compass, and both rule questions."""
import json, os, tempfile, threading
from http.server import BaseHTTPRequestHandler, HTTPServer

os.environ["PENGUPOOL_MUSE_HOME"] = tempfile.mkdtemp()
from pengu import compass, review, rules, model

answers = []  # what the fake model says next, in order


class Fake(BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        body = json.dumps({"choices": [{"message": {"content": answers.pop(0)}}]}).encode()
        self.send_response(200); self.end_headers(); self.wfile.write(body)

    def log_message(self, *a): pass


srv = HTTPServer(("127.0.0.1", 0), Fake)
threading.Thread(target=srv.serve_forever, daemon=True).start()
os.environ["MUSE_BASE_URL"] = f"http://127.0.0.1:{srv.server_port}/v1"

# A rule, wrapped in a code fence like models do.
answers.append('```json\n{"is_rule": true, "rule": "Run the test suite without asking."}\n```')
assert rules.extract("from now on you can run tests without asking") == "Run the test suite without asking."
# A one-off task is not a rule.
answers.append('{"is_rule": false, "rule": ""}')
assert rules.extract("fix the login bug") is None

line = compass.add("Run the test suite without asking.", "typed")
assert [l["id"] for l in compass.load()] == [line["id"]]

call = {"tool": "Bash", "input": {"command": "pytest -q"}}
answers.append(json.dumps({"allowed_by": line["id"], "reason": "tests"}))
assert rules.allowed_by(call, compass.load()) == line["id"]
answers.append('{"allowed_by": "made-up", "reason": "x"}')  # an invented id allows nothing
assert rules.allowed_by(call, compass.load()) is None
assert rules.allowed_by(call, []) is None  # no lines: no model call at all

answers.append("I cannot help with that.")
try:
    rules.extract("anything"); raise AssertionError("prose must fail")
except model.ModelError:
    pass

assert compass.forget(line["id"]) and compass.load() == [] and not compass.forget(line["id"])

# review: empty diff refused; prompt via --prompt-file with read-only flags and an
# isolated cwd; the diff is framed as untrusted data; nonzero/empty raise.
try:
    review.review_diff("   "); raise AssertionError("empty diff must fail")
except ValueError:
    pass
import subprocess as _sp
_real_run = _sp.run
_seen = {}
def _fake_ok(*a, **k):
    _seen.update(argv=a[0], kwargs=k)
    with open(a[0][3], encoding="utf-8") as f:
        _seen["prompt"] = f.read()
    import os as _os
    assert _os.listdir(k["cwd"]) == ["prompt.md"], "reviewer cwd holds only the prompt"
    return type("P", (), {"returncode": 0, "stdout": "no issues", "stderr": "warn"})()
_sp.run = _fake_ok
try:
    assert review.review_diff("diff --git a/x b/x") == "no issues"
finally:
    _sp.run = _real_run
assert _seen["argv"][:3] == ["muse", "exec", "--prompt-file"]
assert "--disable-write" in _seen["argv"] and "--disable-shell" in _seen["argv"]
assert _seen["kwargs"].get("capture_output") is True
assert _seen["kwargs"].get("encoding") == "utf-8"
assert "UNTRUSTED" in _seen["prompt"] and "diff --git a/x b/x" in _seen["prompt"]
for bad, fn in [(1, lambda *a, **k: type("P", (), {"returncode": 1, "stdout": "", "stderr": "boom"})()),
                (0, lambda *a, **k: type("P", (), {"returncode": 0, "stdout": "  ", "stderr": "log"})())]:
    _sp.run = fn
    try:
        try:
            review.review_diff("diff"); raise AssertionError("must fail")
        except RuntimeError:
            pass
    finally:
        _sp.run = _real_run
big = "x" * (review.MAX_DIFF_CHARS + 10)
_sp.run = _fake_ok
try:
    review.review_diff(big)
finally:
    _sp.run = _real_run
assert "truncated" in _seen["prompt"] and big not in _seen["prompt"]

# review CLI branches: rev uses git show, a pipe uses stdin, else worktree vs HEAD.
import sys as _sys
from pengu import __main__ as _cli
_got = {}
def _fake_review(d):
    _got["diff"] = d
    return "no issues"
_cli.review.review_diff = _fake_review
_real_git = _cli._git_out
_real_argv, _real_stdin = _sys.argv, _sys.stdin
class _Stdin:
    def __init__(self, tty, data=""):
        self._tty, self._data = tty, data
    def isatty(self):
        return self._tty
    def read(self):
        return self._data
try:
    def _fake_git(argv):
        _got["git"] = argv
        return "SHOWDIFF"
    _cli._git_out = _fake_git
    _sys.argv = ["pengu", "review", "HEAD~1"]
    _cli.main()
    assert _got == {"git": ["git", "show", "HEAD~1", "--"], "diff": "SHOWDIFF"}, _got
    _got.clear()
    _sys.argv = ["pengu", "review"]
    _sys.stdin = _Stdin(False, "PIPEDIFF")
    _cli.main()
    assert _got == {"diff": "PIPEDIFF"}, _got
    _got.clear()
    _sys.stdin = _Stdin(True)
    _cli._git_out = lambda argv: "WDIFF"
    _cli.main()
    assert _got == {"diff": "WDIFF"}, _got
    try:
        _cli._git_out = lambda argv: _sys.exit("git failed: no git")
        _sys.stdin = _Stdin(True)
        _cli.main(); raise AssertionError("git failure must exit")
    except SystemExit as e:
        assert "git failed" in str(e.code)
finally:
    _cli._git_out = _real_git
    _sys.argv, _sys.stdin = _real_argv, _real_stdin
print("ok")

# sessions: bad agent rejected; lifecycle runs against a fake tmux.
import tempfile as _tf
from pengu import session as _sess
import os as _o
_real_home = _o.environ.get("PENGUPOOL_MUSE_HOME")
_o.environ["PENGUPOOL_MUSE_HOME"] = _tf.mkdtemp()
try:
    _sess.new("bogus"); raise AssertionError("bad agent must fail")
except ValueError:
    pass
_real_which = _sess.shutil.which
_real_tmux = _sess._tmux
_real_alive = _sess._tmux_alive
_sess.shutil.which = lambda c: f"/bin/{c}"
_calls = []
class _P:
    def __init__(self, rc=0):
        self.returncode, self.stderr = rc, ""
def _fake_tmux(*a, **k):
    _calls.append(a)
    return _P()
_sess._tmux = _fake_tmux
_sess._tmux_alive = lambda name: True
import shlex as _sx
try:
    row = _sess.new("muse", "hi")
    assert row["agent"] == "muse" and row["tmux"].startswith("pengu-")
    assert "--trust-workspace" not in _calls[0][-1]  # explicit opt-in only
    assert _calls[1][:2] == ("pipe-pane", "-o") and _calls[1][-1].startswith("cat >> ")
    assert _sess.load() and _sess.ls()[0]["alive"] is True
    assert _sess.stop(row["id"]) is True and _sess.load() == []
    assert _sess.stop(row["id"]) is False
    _calls.clear()
    trow = _sess.new("muse", "a;b", trust_workspace=True)
    tcmd = _calls[0][-1]
    assert "--trust-workspace" in tcmd and _sx.quote("a;b") in tcmd
    _sess.stop(trow["id"])
    _calls.clear()
    crow = _sess.new("claude", "do;rm -rf /")
    cmd = _calls[0][-1]
    assert "--settings" in cmd and _sx.quote("do;rm -rf /") in cmd  # task quoted, not raw
    settings = [c.strip("'") for c in cmd.split() if "settings.json" in c]
    assert len(settings) == 1 and _o.path.exists(settings[0]), cmd
    _sess.stop(crow["id"])
    # a Compass line rides on the task (server instructions miss the prompt).
    compass.add("Probe line one.", "typed")
    _calls.clear()
    prow = _sess.new("muse", "do it", trust_workspace=True)
    assert "Probe line one." in _calls[0][-1] and _calls[0][-1].rstrip().endswith("do it'")
    _sess.stop(prow["id"])
    _sess._tmux = lambda *a, **k: _P(rc=1)
    try:
        _sess.new("claude"); raise AssertionError("tmux failure must fail")
    except RuntimeError:
        pass
    # pipe-pane failure kills the new session: no orphan, no row.
    _calls.clear()
    def _fail_pipe(*a, **k):
        _calls.append(a)
        return _P(rc=1 if a[0] == "pipe-pane" else 0)
    _sess._tmux = _fail_pipe
    try:
        _sess.new("muse", "x"); raise AssertionError("pipe-pane failure must fail")
    except RuntimeError as e:
        assert "pipe-pane" in str(e)
    assert _calls[-1][0] == "kill-session" and _sess.load() == []
    # stop drops the row even when tmux itself is down.
    _sess._tmux = _fail_pipe
    srow = {"id": "deadbeef", "agent": "muse", "tmux": "pengu-deadbeef", "task": "",
            "log": "x", "started": "x"}
    _sess._save([srow])
    def _boom(*a, **k):
        raise RuntimeError("tmux down")
    _sess._tmux = _boom
    assert _sess.stop("deadbeef") is True and _sess.load() == []
    _sess._tmux = _fail_pipe
finally:
    _sess._tmux = _real_tmux
    _sess._tmux_alive = _real_alive
    _sess.shutil.which = _real_which
    if _real_home is None:
        del _o.environ["PENGUPOOL_MUSE_HOME"]
    else:
        _o.environ["PENGUPOOL_MUSE_HOME"] = _real_home
print("ok")

# CLI dispatch: new/ls/stop reach session with the parsed flags.
_hit = {}
_rn, _rl, _rs = _cli.session.new, _cli.session.ls, _cli.session.stop
def _fake_new(*a, **k):
    _hit["new"] = (a, k)
    return {"id": "x1", "agent": a[0], "tmux": "pengu-x1"}
def _fake_ls():
    _hit["ls"] = True
    return [{"id": "x1", "agent": "muse", "alive": True, "task": "t"}]
def _fake_stop(sid):
    _hit["stop"] = sid
    return True
_cli.session.new, _cli.session.ls, _cli.session.stop = _fake_new, _fake_ls, _fake_stop
try:
    _sys.argv = ["pengu", "new", "--agent", "muse", "--task", "hi", "--trust-workspace"]
    _cli.main()
    assert _hit["new"] == (("muse", "hi"), {"trust_workspace": True}), _hit
    _sys.argv = ["pengu", "ls"]
    _cli.main()
    assert _hit["ls"] is True
    _sys.argv = ["pengu", "stop", "x1"]
    _cli.main()
    assert _hit["stop"] == "x1"
    _sys.argv = ["pengu", "new", "--agent", "claude", "--trust-workspace"]
    try:
        _cli.main(); raise AssertionError("--trust-workspace with claude must fail")
    except SystemExit as e:
        assert e.code == 2
    _rc, _rs2 = _cli.chat.send, _cli.chat.inbox
    def _fake_send(f, t, x):
        _hit["send"] = (f, t, x)
        return {"id": "m1", "from": f, "to": t}
    def _fake_inbox(sid):
        _hit["inbox"] = sid
        return []
    _cli.chat.send, _cli.chat.inbox = _fake_send, _fake_inbox
    try:
        _sys.argv = ["pengu", "send", "--from", "a", "--to", "b", "--text", "hi"]
        _cli.main()
        assert _hit["send"] == ("a", "b", "hi"), _hit
        _sys.argv = ["pengu", "inbox", "b"]
        _cli.main()
        assert _hit["inbox"] == "b"
    finally:
        _cli.chat.send, _cli.chat.inbox = _rc, _rs2
    _ro, _rw, _rp, _rpl = (_cli.owner.create_question, _cli.owner.wait_answer,
                           _cli.owner.main_serve, _cli.owner.list_proposals)
    def _fake_ask(*a):
        _hit["ask"] = a
        return {"id": "ask1"}
    _cli.owner.create_question = _fake_ask
    _cli.owner.wait_answer = lambda qid, t: ("yes", None)
    def _fake_serve(b, p):
        _hit["serve"] = (b, p)
    def _fake_props():
        _hit["props"] = True
        return [{"at": 1.0, "words": "w", "status": "proposed"}]
    _cli.owner.main_serve, _cli.owner.list_proposals = _fake_serve, _fake_props
    try:
        _sys.argv = ["pengu", "ask", "--session", "s", "--question", "Q?",
                     "--option", "A", "--option", "B", "--expires", "10"]
        _cli.main()
        assert _hit["ask"] == ("s", "Q?", ["A", "B"], 10), _hit
        _sys.argv = ["pengu", "owner-serve", "--bind", "127.0.0.1", "--port", "9999"]
        _cli.main()
        assert _hit["serve"] == ("127.0.0.1", 9999), _hit
        _sys.argv = ["pengu", "proposals"]
        _cli.main()
        assert _hit["props"] is True
        _cli.owner.wait_answer = lambda qid, t: (None, "timeout")
        _sys.argv = ["pengu", "ask", "--session", "s", "--question", "Q?",
                     "--option", "A", "--option", "B"]
        try:
            _cli.main(); raise AssertionError("timeout must exit nonzero")
        except SystemExit as e:
            assert e.code != 0 and "still waiting" in str(e.code)
    finally:
        (_cli.owner.create_question, _cli.owner.wait_answer, _cli.owner.main_serve,
         _cli.owner.list_proposals) = (_ro, _rw, _rp, _rpl)
finally:
    _cli.session.new, _cli.session.ls, _cli.session.stop = _rn, _rl, _rs
    _sys.argv = _real_argv
print("ok")

# mcp: initialize carries the lines as instructions; tools and resources work;
# unknown methods/frames fail cleanly; notifications get no reply.
from pengu import mcp as _mcp
_o.environ["PENGUPOOL_MUSE_HOME"] = _tf.mkdtemp()
compass.add("Always run the test suite before committing.", "typed")
compass.add("Never touch the migrations folder.", "typed")
r = _mcp.handle({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
assert r["result"]["serverInfo"]["name"] == "pengu-compass"
assert "Always run the test suite" in r["result"]["instructions"]
assert "data, not commands" in r["result"]["instructions"]
tools = _mcp.handle({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})["result"]["tools"]
assert {t["name"] for t in tools} == {"compass_context", "compass_observe"}
hit = _mcp.handle({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                    "params": {"name": "compass_context",
                               "arguments": {"topic": "migrations"}}})
assert "migrations folder" in hit["result"]["content"][0]["text"]
assert "test suite" not in hit["result"]["content"][0]["text"]
miss = _mcp.handle({"jsonrpc": "2.0", "id": 4, "method": "tools/call",
                     "params": {"name": "compass_context", "arguments": {}}})
assert "test suite" in miss["result"]["content"][0]["text"]
note = _mcp.handle({"jsonrpc": "2.0", "id": 5, "method": "tools/call",
                     "params": {"name": "compass_observe", "arguments": {"words": "x"}}})
assert "owner confirms" in note["result"]["content"][0]["text"]
assert compass.load() and len(compass.load()) == 2  # observe writes nothing
assert _mcp.handle({"jsonrpc": "2.0", "id": 6, "method": "tools/call",
                     "params": {"name": "nope"}})["error"]["code"] == -32602
res = _mcp.handle({"jsonrpc": "2.0", "id": 7, "method": "resources/list"})
assert res["result"]["resources"][0]["uri"] == "compass://lines"
got = _mcp.handle({"jsonrpc": "2.0", "id": 8, "method": "resources/read",
                    "params": {"uri": "compass://lines"}})
assert "migrations folder" in got["result"]["contents"][0]["text"]
assert _mcp.handle({"jsonrpc": "2.0", "id": 9, "method": "resources/read",
                     "params": {"uri": "compass://nope"}})["error"]["code"] == -32002
assert _mcp.handle({"method": "notifications/initialized"}) is None
assert _mcp.handle({"jsonrpc": "2.0", "id": 12, "method": "notifications/fake"}) is None
assert _mcp.handle({"jsonrpc": "2.0", "method": "tools/list"}) is None  # no id: no reply
assert _mcp.handle({"jsonrpc": "2.0", "id": 10, "method": "frobnicate"})["error"]["code"] == -32601
bad = _mcp.handle({"jsonrpc": "2.0", "id": 11, "method": "tools/call", "params": "x"})
assert bad["error"]["code"] == -32602 and "params" in bad["error"]["message"]
badargs = _mcp.handle({"jsonrpc": "2.0", "id": 12, "method": "tools/call",
                        "params": {"name": "compass_context", "arguments": [1]}})
assert badargs["error"]["code"] == -32602
badt = _mcp.handle({"jsonrpc": "2.0", "id": 13, "method": "tools/call",
                     "params": {"name": "compass_context", "arguments": {"topic": 1}}})
assert badt["error"]["code"] == -32602 and "topic" in badt["error"]["message"]
nowords = _mcp.handle({"jsonrpc": "2.0", "id": 14, "method": "tools/call",
                        "params": {"name": "compass_observe", "arguments": {}}})
assert nowords["error"]["code"] == -32602 and "words" in nowords["error"]["message"]
echo = _mcp.handle({"jsonrpc": "2.0", "id": 15, "method": "initialize",
                     "params": {"protocolVersion": "1999-01-01"}})
assert echo["result"]["protocolVersion"] == "2024-11-05"  # server's version, not the client's
# serve_stdio framing: blank lines skipped, bad frames answered, notifications silent.
import io as _io
_frames = ('{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}\n'
           '\n'
           'not json\n'
           '{"method": "notifications/initialized"}\n')
_real_stdin, _real_stdout = _sys.stdin, _sys.stdout
_sys.stdin, _sys.stdout = _io.StringIO(_frames), _io.StringIO()
try:
    _mcp.serve_stdio()
    out = [json.loads(l) for l in _sys.stdout.getvalue().splitlines()]
finally:
    _sys.stdin, _sys.stdout = _real_stdin, _real_stdout
assert [r.get("id") for r in out] == [1, None] and out[1]["error"]["code"] == -32700
# a malformed store never breaks the server: garbage lines are skipped.
with open(compass.path(), "a", encoding="utf-8") as f:
    f.write("half-written{\n42\n[]\n")
assert len(compass.load()) == 2
assert "test suite" in _mcp.handle({"jsonrpc": "2.0", "id": 21, "method": "initialize",
                                     "params": {}})["result"]["instructions"]
if _real_home is None:
    del _o.environ["PENGUPOOL_MUSE_HOME"]
else:
    _o.environ["PENGUPOOL_MUSE_HOME"] = _real_home
print("ok")

# chat: turns recorded, inbox per recipient, off-tree refused, pane nudge best-effort.
from pengu import chat as _chat
_o.environ["PENGUPOOL_MUSE_HOME"] = _tf.mkdtemp()
_ct, _ca = _sess._tmux, _sess._tmux_alive
_sess._save([{"id": "aaa", "agent": "claude", "tmux": "pengu-aaa", "task": "",
              "log": "x", "started": "x"},
             {"id": "bbb", "agent": "muse", "tmux": "pengu-bbb", "task": "",
              "log": "x", "started": "x"}])
_sess._tmux_alive = lambda name: True
_nudge_calls = []
def _fake_nudge(*a, **k):
    _nudge_calls.append(a)
    raise RuntimeError("tmux down")
_sess._tmux = _fake_nudge
try:
    e1 = _chat.send("aaa", "bbb", "please review the diff")
    assert e1["from"] == "aaa" and e1["to"] == "bbb"  # nudge failure doesn't fail send
    assert _nudge_calls[-1][:5] == ("send-keys", "-t", "pengu-bbb", "-l",
                                    "[pengu] message from aaa; run: pengu inbox bbb")
    assert [e["text"] for e in _chat.inbox("bbb")] == ["please review the diff"]
    e2 = _chat.send("bbb", "aaa", "no issues")
    assert [e["text"] for e in _chat.inbox("aaa")] == ["no issues"]
    assert _chat.inbox("owner") == []
    try:
        _chat.send("aaa", "zzz", "x"); raise AssertionError("off-tree must fail")
    except ValueError:
        pass
    try:
        _chat.send("aaa", "bbb", "  "); raise AssertionError("empty must fail")
    except ValueError:
        pass
    _real_stdin_now = _sys.stdin
    class _NotTty:
        def isatty(self):
            return False
    _sys.stdin = _NotTty()
    try:
        _chat.send("owner", "aaa", "x"); raise AssertionError("owner send needs a terminal")
    except ValueError:
        pass
    finally:
        _sys.stdin = _real_stdin_now
    # entries older than 30 days are pruned on send.
    with open(_chat._chat_path(), "a", encoding="utf-8") as f:
        f.write(json.dumps({"id": "old", "at": "2020-01-01", "ts": 0,
                            "from": "aaa", "to": "bbb", "text": "old"}) + "\n")
    _chat.send("aaa", "bbb", "new")
    texts = [e["text"] for e in _chat.inbox("bbb")]
    assert "old" not in texts and "new" in texts
finally:
    _sess._tmux, _sess._tmux_alive = _ct, _ca
print("ok")

# owner API: real HTTP on localhost; auth, long-poll, answer rules, idempotency, proposals.
import threading as _th
import urllib.request as _ur
from pengu import owner as _own
_o.environ["PENGUPOOL_OWNER_TOKEN"] = "test-token"
_o.environ["PENGUPOOL_MUSE_HOME"] = _tf.mkdtemp()
_sess._save([{"id": "s1", "agent": "claude", "tmux": "pengu-s1", "task": "",
              "log": "x", "started": "x"}])
_srv = _own.serve("127.0.0.1", 0)
_th.Thread(target=_srv.serve_forever, daemon=True).start()
_base = f"http://127.0.0.1:{_srv.server_port}"
def _call(method, path, body=None, token="test-token", raw=False):
    req = _ur.Request(_base + path, method=method,
                      headers={"Authorization": f"Bearer {token}"})
    try:
        with _ur.urlopen(req, data=json.dumps(body).encode() if body else None,
                         timeout=10) as r:
            return r.status, json.loads(r.read() or b"null")
    except _ur.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")
q = _own.create_question("s1", "Ship it?", ["yes", "no"], expires_in=60)
for bad_q in (["only"], ["a", "a"], ["", "b"], "yesno", ["a", 1]):
    try:
        _own.create_question("s1", "Bad?", bad_q); raise AssertionError(f"must fail: {bad_q}")
    except ValueError:
        pass
for bad_c in [("zzz", "Q?", ["a", "b"]), ("s1", "", ["a", "b"])]:
    try:
        _own.create_question(*bad_c); raise AssertionError(f"must fail: {bad_c}")
    except ValueError:
        pass
_saved_token = _o.environ.pop("PENGUPOOL_OWNER_TOKEN")
try:
    _own.serve("127.0.0.1", 0); raise AssertionError("missing token must fail closed")
except RuntimeError:
    pass
finally:
    _o.environ["PENGUPOOL_OWNER_TOKEN"] = _saved_token
try:
    code, items = _call("GET", "/questions?wait=0")
    assert code == 200 and [i["id"] for i in items] == [q["id"]]
    assert _call("POST", "/proposals", {"words": "x"}, token="wrong")[0] == 401
    assert _call("GET", "/questions?wait=0", token="wrong")[0] == 401
    assert _call("POST", f"/questions/{q['id']}/answer", None, token="wrong")[0] == 401
    ans = {"option": "yes", "payload_hash": q["payload_hash"], "idempotency_key": "k1"}
    code, got = _call("POST", f"/questions/{q['id']}/answer", ans)
    assert code == 200 and got["option"] == "yes", (code, got)
    code, got2 = _call("POST", f"/questions/{q['id']}/answer", ans)  # idempotent replay
    assert code == 200 and got2 == got
    changed = dict(ans, option="no", payload_hash="deadbeef", idempotency_key="k1")
    code, got3 = _call("POST", f"/questions/{q['id']}/answer", changed)
    assert (code, got3) == (200, got)  # same key replays the first result, not the new body
    other = dict(ans, option="no", idempotency_key="k2")
    assert _call("POST", f"/questions/{q['id']}/answer", other)[0] == 409
    bad = _own.create_question("s1", "Q?", ["a", "b"], expires_in=60)
    wrong = dict(ans, payload_hash="deadbeef", idempotency_key="k3")
    assert _call("POST", f"/questions/{bad['id']}/answer", wrong)[0] == 409
    assert _call("POST", f"/questions/{bad['id']}/answer",
                  {"option": "zzz", "payload_hash": bad["payload_hash"],
                   "idempotency_key": "k4"})[0] == 400
    # errors replay too: the same key returns the first 409.
    code, body = _call("POST", f"/questions/{bad['id']}/answer", wrong)
    assert code == 409 and body["error"] == "changed"
    # non-object JSON and non-string words are 400, not crashes.
    assert _call("POST", f"/questions/{bad['id']}/answer", [1, 2])[0] == 400
    assert _call("POST", "/proposals", {"words": 42})[0] == 400
    code, props = _call("POST", "/proposals", {"words": "prefer small commits"})
    assert code == 202, (code, props)
    code, listed = _call("GET", "/proposals")
    assert code == 200 and any(p["words"] == "prefer small commits" for p in listed)
    assert _call("POST", "/questions/nope/answer", dict(ans, idempotency_key="k404"))[0] == 404
    assert _call("POST", f"/questions/{bad['id']}/answer",
                  {"option": "a", "payload_hash": bad["payload_hash"]})[0] == 400
    assert _call("GET", "/questions?wait=nan")[0] == 400
    assert _call("GET", "/questions?wait=inf")[0] == 400
    exp = _own.create_question("s1", "Old?", ["a", "b"], expires_in=60)
    with _sess._locked():
        st = _own._load_state()
        st["questions"][exp["id"]]["expires_at"] = 1.0  # long past, no sleep
        _own._save_state(st)
    assert _call("POST", f"/questions/{exp['id']}/answer",
                  {"option": "a", "payload_hash": exp["payload_hash"],
                   "idempotency_key": "kexp"})[0] == 409
    assert _own.wait_answer(q["id"], 0.1) == ("yes", None)
    # long-poll releases when a question lands.
    _own.answer_question(bad["id"], "a", bad["payload_hash"], "k-late")
    def _ask_late():
        import time as _t
        _t.sleep(0.6)
        _own.create_question("s1", "Late?", ["y", "n"], expires_in=60)
    _th.Thread(target=_ask_late, daemon=True).start()
    code, items = _call("GET", "/questions?wait=5")
    assert code == 200 and any(i["question"] == "Late?" for i in items)
    # a corrupt state file is backed up, not wiped.
    _own._save_state({"questions": {}, "idempotency": {}, "proposals": []})
    _own._state_path().write_text("{broken", encoding="utf-8")
    assert _own.pending_questions() == []
    left = list(_own._state_path().parent.glob("owner.corrupt-*.json"))
    assert len(left) == 1 and left[0].read_text(encoding="utf-8") == "{broken"
    left[0].unlink()
    # malformed inner rows are skipped, not fatal.
    _own._save_state({"questions": {"q1": [1, 2], "q2": {"status": "pending"}},
                      "idempotency": {"k": [1]},
                      "proposals": [[1], {"words": "w"},
                                    {"words": "w2", "at": 9999999999.0, "status": "proposed"}]})
    assert _own.pending_questions() == []
    assert [p["words"] for p in _own.list_proposals()] == ["w2"]
    assert _own.wait_answer("missing", 0.1) == (None, "no such question")
    q2 = _own.create_question("s1", "Slow?", ["a", "b"], expires_in=60)
    assert _own.wait_answer(q2["id"], 0) == (None, "timeout")  # pending, waiter gave up
    def _late():
        import time as _t
        _t.sleep(0.6)
        _own.answer_question(q2["id"], "a", q2["payload_hash"], "k5")
    _th.Thread(target=_late, daemon=True).start()
    assert _own.wait_answer(q2["id"], 5) == ("a", None)  # a posted answer releases the waiter
    q3 = _own.create_question("s1", "Never?", ["a", "b"], expires_in=1)
    assert _own.wait_answer(q3["id"], 3) == (None, "expired")
    # a non-loopback bind needs explicit opt-in.
    try:
        _own.serve("0.0.0.0", 0); raise AssertionError("public bind must fail closed")
    except RuntimeError:
        pass
finally:
    _srv.shutdown()
    if _real_home is None:
        del _o.environ["PENGUPOOL_MUSE_HOME"]
    else:
        _o.environ["PENGUPOOL_MUSE_HOME"] = _real_home
    del _o.environ["PENGUPOOL_OWNER_TOKEN"]
print("ok")

# send-to-owner lands in the owner's inbox; no pane nudge is attempted.
_o.environ["PENGUPOOL_MUSE_HOME"] = _tf.mkdtemp()
_sess._save([{"id": "aaa", "agent": "claude", "tmux": "pengu-aaa", "task": "",
              "log": "x", "started": "x"}])
_ct2, _ca2 = _sess._tmux, _sess._tmux_alive
_sess._tmux = lambda *a, **k: (_ for _ in ()).throw(AssertionError("no nudge for owner"))
_sess._tmux_alive = lambda name: (_ for _ in ()).throw(AssertionError("no lookup for owner"))
try:
    _chat.send("aaa", "owner", "should I ship it?")
    assert [e["text"] for e in _chat.inbox("owner")] == ["should I ship it?"]
finally:
    _sess._tmux, _sess._tmux_alive = _ct2, _ca2
    if _real_home is None:
        del _o.environ["PENGUPOOL_MUSE_HOME"]
    else:
        _o.environ["PENGUPOOL_MUSE_HOME"] = _real_home
print("ok")
