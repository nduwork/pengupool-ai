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
import sys, json, threading, os, subprocess, socket
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
        if self.path == "/api/healthz":
            assert self.headers.get("Authorization") is None, "healthz sends no token"
            return self._send(200, {"epoch": 7}, chunked=True)
        assert self.headers["Authorization"] == "Bearer tok123", self.headers
        if self.path == "/api/muse/v1/next": return self._send(200, {"ask_id": "teamask:a1", "payload_hash": "ph", "actions": []}, chunked=True)
        if self.path == "/api/muse/v1/sessions": return self._send(200, [{"session": "s1"}], chunked=True)
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
os.environ.update(PENGUPOOL_HOST="127.0.0.1", PENGUPOOL_PORT=str(srv.server_address[1]))
os.environ.pop("HTTPS_PROXY", None); os.environ.pop("https_proxy", None)
sys.path.insert(0, os.environ["SRC"])
import pp
assert pp.healthz() == (200, {"epoch": 7})
psrv = socket.socket(); psrv.bind(("127.0.0.1", 0)); psrv.listen(1)
def fake407():
    c, _ = psrv.accept()
    c.recv(4096)
    c.sendall(b"HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
    c.close()
threading.Thread(target=fake407, daemon=True).start()
os.environ["HTTPS_PROXY"] = "http://127.0.0.1:%d" % psrv.getsockname()[1]
try:
    pp.connect()
    raise AssertionError("a 407 is the proxy refusing: fail fast, do not try 3130")
except SystemExit as e:
    assert "407" in str(e), e
finally:
    os.environ.pop("HTTPS_PROXY", None)
psrv.close()
run = lambda *a: subprocess.run([sys.executable, os.environ["SRC"] + "/pp.py", *a], env=env, capture_output=True, text=True)
r = run("answer", "teamask:a1", "ph", "reply", "Use", "8080"); assert r.returncode == 0, r.stderr
r = run("answer", "teamask:a1"); assert r.returncode != 0 and "usage" in r.stderr, r
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
 {"session":"lead1","title":"lead · app","name":"lead","state":"running","status":"working","waiting":False,"team":"app","team_id":"t1","role":"LEAD"},
 {"session":"web1","title":"web <b> · app","name":"web <b>","state":"running","status":"idle","waiting":True,"team":"app","team_id":"t1","role":"HELPER","parent":"lead1"},
 {"session":"solo1","title":"solo","state":"exited","waiting":False},
]
def fake(method, path, body=None):
    if path == "/sessions": return 200, SESS
    if path == "/next": return 200, {"session":"web1","title":"web","description":"Which port?","ask_id":"teamask:x","payload_hash":"h","actions":[]}
    raise AssertionError(path)
pp.api = fake
pp.healthz = lambda: (200, {"epoch": 11})
import gadget, detect
out = gadget.render()
assert "web &lt;b&gt;" in out and "? Need input" in out and "Active" in out and "Ended" in out, out
assert out.index("lead") < out.index("web &lt;b&gt;") and "padding-left:32px" in out, "web under lead"
assert "Ungrouped" in out and "badge" not in out and "· app" not in out, "rows show the name"
obs = detect.observe()
assert obs["reachable"] and obs["ask"] and obs["sessions"]["web1"]["status"] == "waiting on you" and obs["sessions"]["lead1"]["status"] == "working", obs
assert obs["epoch"] == 11, obs
pp.api = lambda method, path, body=None: (401, {"error": "unauthorized"})
obs = detect.observe()
assert obs["refused"] and not obs["reachable"] and not obs["sessions"], obs
pp.api = lambda method, path, body=None: (200, []) if path == "/sessions" else (204, None)
obs = detect.observe()
assert obs["reachable"] and not obs["sessions"] and not obs["ask"], "no sessions is not unreachable"
def down(): raise OSError("daemon down")
pp.healthz = down
obs = detect.observe()
assert obs["epoch"] is None and obs["reachable"], "healthz failing is no signal"
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
