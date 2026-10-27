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
