"""python3 test_muse.py: the client against a local fake endpoint, the Compass, and both rule questions."""
import json, os, tempfile, threading
from http.server import BaseHTTPRequestHandler, HTTPServer

os.environ["PENGUPOOL_MUSE_HOME"] = tempfile.mkdtemp()
from muse import compass, rules, model

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
print("ok")
