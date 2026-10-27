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
