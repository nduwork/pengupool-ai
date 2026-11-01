#!/usr/bin/env python3
"""Local tests for the companion's device-API client (pp.py).

No daemon and no network egress needed:
  - HTTP-layer tests run pp._request over a socketpair with a scripted
    server thread (the tunnel is stubbed).
  - The Serve TLS test stubs only the proxy CONNECT hop, then runs the
    real _tunnel_serve TLS wrap against a local TLS server whose cert
    chains to a throwaway CA (PENGUPOOL_CA_FILE).
  - Smoke tests cover askcard rendering and the tracker helpers.

Run: python3 -m unittest discover -s tests -v   (from the package root)
"""
import json
import os
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import pp  # noqa: E402

TOKEN = "test-token-0123456789"


def run_server(sock, response, captured):
    """Read one HTTP request from sock, record it, write response, close."""
    data = b""
    while b"\r\n\r\n" not in data:
        chunk = sock.recv(4096)
        if not chunk:
            break
        data += chunk
    head, _, rest = data.partition(b"\r\n\r\n")
    length = 0
    for line in head.decode("latin1").split("\r\n")[1:]:
        if line.lower().startswith("content-length:"):
            length = int(line.split(":", 1)[1].strip())
    body = rest
    while len(body) < length:
        body += sock.recv(4096)
    captured["request"] = head.decode("latin1")
    captured["body"] = body.decode("latin1")
    sock.sendall(response)
    sock.close()


def socketpair_tunnel(response, captured):
    client_end, server_end = socket.socketpair()
    t = threading.Thread(target=run_server, args=(server_end, response, captured), daemon=True)
    t.start()
    return lambda: pp.Sock(client_end)


class HttpLayerTests(unittest.TestCase):
    def setUp(self):
        pp.load_device_token = lambda: TOKEN
        self._transport = pp.TRANSPORT

    def tearDown(self):
        pp.TRANSPORT = self._transport

    def test_get_sessions_serve_mode(self):
        pp.TRANSPORT = "serve"
        captured = {}
        body = json.dumps([{"session": "s1", "name": "agent"}]).encode()
        resp = b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
        pp.tunnel = socketpair_tunnel(resp, captured)
        status, out = pp.device_get("/sessions")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(out)[0]["name"], "agent")
        req = captured["request"]
        self.assertIn("GET /api/muse/v1/sessions HTTP/1.1", req)
        self.assertIn(f"Authorization: Bearer {TOKEN}", req)
        # Serve mode: bare tailnet name, no port suffix.
        self.assertIn(f"Host: {pp.HOST_HEADER}\r\n", req)

    def test_get_sessions_direct_mode_host_header(self):
        pp.TRANSPORT = "direct"
        captured = {}
        body = b"[]"
        resp = b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n" + body
        pp.tunnel = socketpair_tunnel(resp, captured)
        status, _ = pp.device_get("/sessions")
        self.assertEqual(status, 200)
        self.assertIn(f"Host: {pp.HOST_HEADER}:{pp.PORT}\r\n", captured["request"])

    def test_chunked_response(self):
        pp.TRANSPORT = "serve"
        captured = {}
        payload = b'{"epoch": "e1"}'
        resp = (b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"
                + hex(len(payload))[2:].encode() + b"\r\n" + payload + b"\r\n0\r\n\r\n")
        pp.tunnel = socketpair_tunnel(resp, captured)
        status, out = pp.device_get("/compass")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(out)["epoch"], "e1")

    def test_pair_sends_no_auth_and_json_body(self):
        pp.TRANSPORT = "serve"
        captured = {}
        body = b'{"token": "abc"}'
        resp = b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
        pp.tunnel = socketpair_tunnel(resp, captured)
        status, out = pp._request("POST", pp.API + "/pair",
                                  body={"code": "123456", "device_name": "Muse (agent)"},
                                  auth=False)
        self.assertEqual(status, 200)
        self.assertNotIn("Authorization", captured["request"])
        self.assertEqual(json.loads(captured["body"])["code"], "123456")

    def test_status_passthrough_401(self):
        pp.TRANSPORT = "serve"
        captured = {}
        resp = b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n"
        pp.tunnel = socketpair_tunnel(resp, captured)
        status, _ = pp.device_get("/sessions")
        self.assertEqual(status, 401)


class ServeTlsTests(unittest.TestCase):
    """The real _tunnel_serve, with only the proxy CONNECT hop stubbed."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="pp-tls-test-")
        cls.ca = os.path.join(cls.tmp, "ca.pem")
        key = os.path.join(cls.tmp, "ca-key.pem")
        srv_key = os.path.join(cls.tmp, "srv-key.pem")
        srv_csr = os.path.join(cls.tmp, "srv.csr")
        cls.srv_cert = os.path.join(cls.tmp, "srv.pem")
        ext = os.path.join(cls.tmp, "ext.cnf")
        with open(ext, "w") as f:
            f.write("subjectAltName=DNS:workstation.test\n")
        def run(*args):
            subprocess.run(args, check=True, capture_output=True)
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-keyout", key,
            "-out", cls.ca, "-days", "1", "-nodes", "-subj", "/CN=test-ca")
        run("openssl", "req", "-newkey", "rsa:2048", "-keyout", srv_key,
            "-out", srv_csr, "-nodes", "-subj", "/CN=workstation.test")
        run("openssl", "x509", "-req", "-in", srv_csr, "-CA", cls.ca,
            "-CAkey", key, "-CAcreateserial", "-out", cls.srv_cert,
            "-days", "1", "-extfile", ext)
        cls.srv_key = srv_key

    def _serve_once(self):
        """Start a one-shot TLS server; returns (port, captured)."""
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(self.srv_cert, self.srv_key)
        ls = socket.socket()
        ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        ls.bind(("127.0.0.1", 0))
        ls.listen(1)
        port = ls.getsockname()[1]
        captured = {}
        body = json.dumps([{"session": "s1", "name": "via-tls"}]).encode()
        resp = b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body

        def serve():
            conn, _ = ls.accept()
            tls = ctx.wrap_socket(conn, server_side=True)
            captured["sni"] = tls.getpeercert()
            run_server(tls, resp, captured)
            ls.close()

        threading.Thread(target=serve, daemon=True).start()
        return port, captured

    def setUp(self):
        self._saved = (pp.HOST_HEADER, pp.TRANSPORT, pp._connect_proxy, pp.load_device_token)
        pp.load_device_token = lambda: TOKEN
        pp.TRANSPORT = "serve"

    def tearDown(self):
        pp.HOST_HEADER, pp.TRANSPORT, pp._connect_proxy, pp.load_device_token = self._saved
        os.environ.pop("PENGUPOOL_CA_FILE", None)

    def _stub_connect(self, port):
        def stub(host, port_ignored):
            raw = socket.create_connection(("127.0.0.1", port), timeout=10)
            return raw, pp.Sock(raw)
        return stub

    def test_serve_round_trip_with_ca(self):
        pp.HOST_HEADER = "workstation.test"
        os.environ["PENGUPOOL_CA_FILE"] = self.ca
        port, captured = self._serve_once()
        pp._connect_proxy = self._stub_connect(port)
        pp.tunnel = pp._tunnel_serve
        status, out = pp.device_get("/sessions")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(out)[0]["name"], "via-tls")
        self.assertIn(f"Host: workstation.test\r\n", captured["request"])

    def test_serve_rejects_untrusted_cert(self):
        pp.HOST_HEADER = "workstation.test"
        os.environ.pop("PENGUPOOL_CA_FILE", None)  # system roots: our CA is not there
        port, _ = self._serve_once()
        pp._connect_proxy = self._stub_connect(port)
        pp.tunnel = pp._tunnel_serve
        with self.assertRaises(ssl.SSLCertVerificationError):
            pp.device_get("/sessions")


class ServeHttpTests(unittest.TestCase):
    """serve-http: the real _tunnel_serve_http over a stubbed CONNECT,
    plaintext to a socketpair server on the Serve HTTP port (80)."""

    def setUp(self):
        self._saved = (pp.TRANSPORT, pp._connect_proxy, pp.load_device_token)
        pp.load_device_token = lambda: TOKEN
        pp.TRANSPORT = "serve-http"

    def tearDown(self):
        pp.TRANSPORT, pp._connect_proxy, pp.load_device_token = self._saved

    def test_dispatch_port_and_plaintext_round_trip(self):
        captured = {}
        seen = {}
        body = b'[{"session": "s1"}]'
        resp = b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
        client_end, server_end = socket.socketpair()
        threading.Thread(target=run_server, args=(server_end, resp, captured), daemon=True).start()

        def stub(host, port):
            seen["host"], seen["port"] = host, port
            return client_end, pp.Sock(client_end)

        pp._connect_proxy = stub
        pp.tunnel = pp._tunnel_serve_http
        status, out = pp.device_get("/sessions")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(out)[0]["session"], "s1")
        self.assertEqual(seen["port"], 80)
        self.assertEqual(seen["host"], pp.HOST)
        # Plain HTTP via Serve: bare tailnet name as Host, no port suffix.
        self.assertIn(f"Host: {pp.HOST_HEADER}\r\n", captured["request"])
        self.assertIn(f"Authorization: Bearer {TOKEN}", captured["request"])


class SmokeTests(unittest.TestCase):
    def test_askcard_renders_and_escapes(self):
        import askcard
        spec = {"agent": "pengupool-ai-muse", "title": "Push <b>now</b>?",
                "options": [{"label": "Push"}, {"label": "Hold"}]}
        html_out = askcard.render_question(spec)
        self.assertIn("Push &lt;b&gt;now&lt;/b&gt;?", html_out)
        self.assertIn("pengupool-ai-muse", html_out)
        closed = askcard.render_closed(spec, "Push")
        self.assertIn("Push", closed)

    def test_tracker_chain(self):
        import tracker
        data = {"agents": {"a1": {"steps": [
            {"label": "Step one", "status": "done"},
            {"label": "Step two", "status": "current"},
            {"label": "Step three", "status": "todo"}]}}}
        lines = tracker.text_lines(data)
        joined = "\n".join(lines)
        self.assertIn("Step one", joined)
        self.assertIn("Step three", joined)

    def test_tracker_example_loads(self):
        import tracker
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "..", "tracker.example.json")
        data = tracker.load(os.path.abspath(path))
        self.assertIn("agents", data)


if __name__ == "__main__":
    unittest.main()
