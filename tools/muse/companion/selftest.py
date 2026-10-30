#!/usr/bin/env python3
"""Self-test for the companion's device-API code, with no PenguPool needed:
pp.py against a local HTTP server, then gadget.py and detect.py against stubbed calls.

  python3 -I selftest.py
"""
import os
import subprocess
import sys
import tempfile

SRC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "src")

PP_CHECK = r"""
import sys, json, threading, os, subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
got = []
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def _send(self, code, obj=None, chunked=False):
        b = json.dumps(obj).encode() if obj is not None else b""
        self.send_response(code)
        if chunked:
            self.send_header("Transfer-Encoding", "chunked"); self.end_headers()
            self.wfile.write(b"%x\r\n%s\r\n0\r\n\r\n" % (len(b), b)); return
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        assert self.headers["Authorization"] == "Bearer tok123", self.headers
        if self.path == "/api/muse/v1/next": return self._send(200, {"ask_id": "teamask:a1", "payload_hash": "ph", "actions": []}, chunked=True)
        if self.path == "/api/muse/v1/sessions": return self._send(200, [{"session": "s1"}])
        self._send(404, {"error": "no"})
    def do_POST(self):
        n = int(self.headers["Content-Length"]); body = json.loads(self.rfile.read(n))
        got.append((self.path, body))
        if self.path.endswith("/action"): return self._send(200, {"session": "s1", "action": body["action"], "state": "exited"})
        self._send(204)
srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
env = dict(os.environ, PENGUPOOL_HOST="127.0.0.1", PENGUPOOL_PORT=str(srv.server_address[1]), PENGUPOOL_DEVICE_TOKEN="tok123")
env.pop("HTTPS_PROXY", None); env.pop("https_proxy", None)
run = lambda *a: subprocess.run([sys.executable, os.environ["SRC"] + "/pp.py", *a], env=env, capture_output=True, text=True)
r = run("answer", "reply", "Use", "8080", "--ask", "teamask:a1"); assert r.returncode == 0, r.stderr
r = run("answer", "go_on", "--ask", "teamask:other"); assert r.returncode != 0 and "not the ask waiting now" in r.stderr, r
r = run("say", "s 1", "hello"); assert r.returncode == 0, r.stderr
r = run("action", "s1", "stop"); assert r.returncode == 0 and '"exited"' in r.stdout, r
r = run("sessions"); assert '"s1"' in r.stdout, r
assert got[0][0] == "/api/muse/v1/asks/teamask:a1/answer" and got[0][1]["text"] == "Use 8080" and got[0][1]["payload_hash"] == "ph" and got[0][1]["idempotency_key"], got
assert got[1] == ("/api/muse/v1/sessions/s%201/say", {"words": "hello"}), got
assert len(got) == 3, got
print("pp ok")
"""

CARD_CHECK = r"""
import sys, json, os
sys.path.insert(0, os.environ["SRC"])
import pp
SESS = [
 {"session":"lead1","title":"lead","state":"running","status":"working","waiting":False,"team":"app","team_id":"t1","role":"LEAD"},
 {"session":"web1","title":"web <b>","state":"running","status":"idle","waiting":True,"team":"app","team_id":"t1","role":"HELPER","parent":"lead1"},
 {"session":"solo1","title":"solo","state":"exited","waiting":False},
]
def fake(method, path, body=None):
    if path == "/sessions": return 200, SESS
    if path == "/next": return 200, {"session":"web1","title":"web","description":"Which port?","ask_id":"teamask:x","payload_hash":"h","actions":[]}
    raise AssertionError(path)
pp.api = fake
import gadget, detect
out = gadget.render()
assert "web &lt;b&gt;" in out and "? Need input" in out and "Active" in out and "Ended" in out, out
assert out.index("lead") < out.index("web &lt;b&gt;") and "padding-left:32px" in out, "web under lead"
assert "Ungrouped" in out and "badge" not in out
obs = detect.observe()
assert obs["ask"] and obs["sessions"]["web1"]["status"] == "waiting on you" and obs["sessions"]["lead1"]["status"] == "working", obs
print("gadget and detect ok")
"""


def main():
    env = dict(os.environ, SRC=SRC)
    with tempfile.TemporaryDirectory() as home:  # detect and gadget read config and state under HOME
        env["HOME"] = home
        for name, code in (("pp", PP_CHECK), ("cards", CARD_CHECK)):
            r = subprocess.run([sys.executable, "-I", "-c", code], env=env, capture_output=True, text=True)
            sys.stdout.write(r.stdout)
            if r.returncode != 0:
                sys.stderr.write(f"{name} check failed:\n{r.stderr}")
                return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
