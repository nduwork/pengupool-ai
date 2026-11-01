#!/usr/bin/env python3
"""PenguPool device-API client (transport + HTTP helpers).

History: this module was the /ws owner-API client (protocol v1 frames,
daemon bootstrap token). The gateway is local-only now — its host guard
answers 404 to every non-loopback request except /api/healthz and
/api/muse/v1/* — and under the owner's Option A decision this machine
holds only the paired device token. The /ws client is retired; what
remains is the CONNECT tunnel to the workstation plus small helpers for
the device API (/api/muse/v1), which is the companion's only channel:

  pp.device_get("/sessions")        -> (status, body bytes)
  pp.device_get("/next")            -> (status, body bytes)  204 = no ask
  pp.device_post("/sessions/<id>/say", {"words": "..."})
  pp.healthz()                      -> (status, dict)  daemon epoch etc.

The device token is read from ./device-token (mode 0600). The bootstrap
token is never read, held, or sent by this module.

Transports (PENGUPOOL_TRANSPORT):
  serve  (default) — the v0.10.20 path. The daemon binds loopback only
    ("Local network was removed ... use Tailscale Serve"), so the device
    API is reached through Tailscale Serve: CONNECT to the workstation's
    tailnet address on port 443 via the runtime proxy, TLS with SNI =
    the machine's tailnet name (Serve's certificate is publicly trusted
    for that name), then plain HTTP/1.1 with Host = the tailnet name.
  direct — the pre-0.10.20 path: CONNECT to the tailnet address on the
    daemon port (7787) and speak plaintext HTTP with
    Host = <tailnet name>:<port>. Only useful while a daemon build
    still binds a network listener.
PENGUPOOL_CA_FILE overrides the trust roots (tests, private CA).
"""
import json
import os
import socket
import ssl
import sys
from urllib.parse import urlparse

HOST = os.environ.get("PENGUPOOL_HOST", "100.106.40.12")
HOST_HEADER = os.environ.get("PENGUPOOL_HOST_HEADER",
                             "nduworkstation.tail04aac9.ts.net")
PORT = int(os.environ.get("PENGUPOOL_PORT", "7787"))
TRANSPORT = os.environ.get("PENGUPOOL_TRANSPORT", "serve")
SERVE_PORT = int(os.environ.get("PENGUPOOL_SERVE_PORT", "443"))
HERE = os.path.dirname(os.path.abspath(__file__))
API = "/api/muse/v1"


def load_device_token():
    with open(os.path.join(HERE, "device-token")) as f:
        return f.read().strip()


class Sock:
    """Buffered socket reader."""

    def __init__(self, s, buf=b""):
        self.s = s
        self.buf = bytearray(buf)

    def read_exact(self, n):
        while len(self.buf) < n:
            chunk = self.s.recv(65536)
            if not chunk:
                raise EOFError("connection closed")
            self.buf += chunk
        out = bytes(self.buf[:n])
        del self.buf[:n]
        return out

    def read_until(self, sep, limit=65536):
        while True:
            i = self.buf.find(sep)
            if i >= 0:
                out = bytes(self.buf[: i + len(sep)])
                del self.buf[: i + len(sep)]
                return out
            if len(self.buf) > limit:
                raise ValueError("header too large")
            chunk = self.s.recv(4096)
            if not chunk:
                raise EOFError("connection closed")
            self.buf += chunk


def _connect_proxy(host, port):
    """CONNECT through the runtime proxy to host:port.

    Returns (raw_socket, reader) where reader has consumed exactly the
    CONNECT response headers. Raises EOFError if the proxy or the target
    closes the connection — that is the companion's "unreachable" signal.
    """
    proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")
    if not proxy:
        raise SystemExit("HTTPS_PROXY is not set; cannot reach the tunnel proxy")
    p = urlparse(proxy)
    raw = socket.create_connection((p.hostname, 3130), timeout=20)
    req = (
        f"CONNECT {host}:{port} HTTP/1.1\r\n"
        f"Host: {host}:{port}\r\nProxy-Connection: keep-alive\r\n\r\n"
    )
    raw.sendall(req.encode())
    sk = Sock(raw)
    head = sk.read_until(b"\r\n\r\n").decode("latin1")
    status = head.split("\r\n", 1)[0]
    if " 200 " not in status:
        raise SystemExit(f"tunnel CONNECT failed: {status}")
    return raw, sk


def _tls_context():
    cafile = os.environ.get("PENGUPOOL_CA_FILE") or None
    return ssl.create_default_context(cafile=cafile)


def _tunnel_serve():
    """Tailscale Serve path: CONNECT to the tailnet address on 443,
    then TLS with SNI = the tailnet name (Serve's cert covers the name,
    not the address). Bytes the proxy reader may have buffered past the
    CONNECT headers would be lost by the TLS wrap; a CONNECT response
    has no body, so a non-empty buffer means something is wrong — fail
    loudly instead of corrupting the stream."""
    raw, sk = _connect_proxy(HOST, SERVE_PORT)
    if sk.buf:
        raise SystemExit("tunnel: unexpected bytes after CONNECT response")
    tls = _tls_context().wrap_socket(raw, server_hostname=HOST_HEADER)
    return Sock(tls)


def _tunnel_direct():
    _, sk = _connect_proxy(HOST, PORT)
    return sk


def tunnel():
    if TRANSPORT == "direct":
        return _tunnel_direct()
    if TRANSPORT == "serve":
        return _tunnel_serve()
    raise SystemExit(f"unknown PENGUPOOL_TRANSPORT: {TRANSPORT!r}")


def _request(method, path, body=None, auth=True):
    """One HTTP request to the daemon over the tunnel. Returns (status, body bytes)."""
    sk = tunnel()
    payload = b""
    host_header = HOST_HEADER if TRANSPORT == "serve" else f"{HOST_HEADER}:{PORT}"
    headers = [f"Host: {host_header}", "Connection: close"]
    if auth:
        headers.append(f"Authorization: Bearer {load_device_token()}")
    if body is not None:
        payload = json.dumps(body).encode()
        headers.append("Content-Type: application/json")
        headers.append(f"Content-Length: {len(payload)}")
    req = f"{method} {path} HTTP/1.1\r\n" + "\r\n".join(headers) + "\r\n\r\n"
    sk.s.sendall(req.encode() + payload)
    head = sk.read_until(b"\r\n\r\n").decode("latin1")
    status = int(head.split(" ", 2)[1])
    hdrs = {}
    for line in head.split("\r\n")[1:]:
        if ": " in line:
            k, v = line.split(": ", 1)
            hdrs[k.lower()] = v
    if "chunked" in hdrs.get("transfer-encoding", ""):
        out = bytearray()
        while True:
            size_line = sk.read_until(b"\r\n").decode("latin1").strip()
            n = int(size_line.split(";")[0], 16)
            if n == 0:
                sk.read_until(b"\r\n")
                break
            out += sk.read_exact(n)
            sk.read_until(b"\r\n")
        return status, bytes(out)
    n = int(hdrs.get("content-length", "0"))
    return status, sk.read_exact(n) if n else b""


def device_get(path):
    """GET /api/muse/v1<path> with the device token. Returns (status, body)."""
    return _request("GET", API + path)


def device_post(path, body):
    """POST /api/muse/v1<path> with a JSON body. Returns (status, body)."""
    return _request("POST", API + path, body=body)


def healthz():
    """GET /api/healthz (no token; the gateway answers it on the network).

    Returns (status, dict) — the dict carries the daemon epoch, which
    changes at every daemon start and is how the companion notices a
    restart now that settings.get is local-only.
    """
    status, body = _request("GET", "/api/healthz", auth=False)
    try:
        return status, json.loads(body)
    except Exception:
        return status, {}


def sessions():
    """The device-visible sessions, as a list of dicts ( [] on failure )."""
    code, body = device_get("/sessions")
    if code != 200:
        return []
    try:
        return json.loads(body)
    except Exception:
        return []


def next_card():
    """The pending ask card dict, or None when nothing waits (204)."""
    code, body = device_get("/next")
    if code != 200:
        return None
    try:
        return json.loads(body)
    except Exception:
        return None


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "sessions":
        for s in sessions():
            print(json.dumps(s, ensure_ascii=False))
    elif cmd == "next":
        card = next_card()
        print(json.dumps(card, indent=2, ensure_ascii=False) if card else "no ask waiting")
    elif cmd == "healthz":
        code, info = healthz()
        print(code, json.dumps(info, ensure_ascii=False))
    elif cmd == "say":
        code, body = device_post(f"/sessions/{args[0]}/say",
                                 {"words": " ".join(args[1:])})
        print(code, body.decode("utf-8", "replace")[:200])
    else:
        print(f"unknown command: {cmd}\n{__doc__}")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
