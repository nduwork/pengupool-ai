#!/usr/bin/env python3
"""PenguPool device-API client: a paired device's calls on /api/muse/v1.

Usage:
  pp.py sessions                      the sessions this device sees (team, role, status, waiting)
  pp.py next                          the oldest waiting ask, as its card (nothing when none waits)
  pp.py answer ASK_ID PAYLOAD_HASH ACTION [TEXT]
                                      answer the card the owner saw (its ask_id and payload_hash)
                                      with one of its action ids; TEXT for a reply. A card that
                                      changed or was answered meanwhile is refused (409).
  pp.py say SESSION WORDS...          type the words into a session, with Enter
  pp.py action SESSION stop|interrupt|resume
  pp.py compass                       the owner's Compass lines

The paired device token is read from ./device-token (mode 0600) or $PENGUPOOL_DEVICE_TOKEN.
It is the only credential: the owner's app token never works on this API.

PENGUPOOL_PORT=443 speaks HTTPS to the Tailscale Serve name, end to end through any proxy. On
plain HTTP (port 7787) a proxy on the way ($HTTPS_PROXY) sees the token and every call.
"""
import json
import os
import socket
import ssl
import sys
import uuid
from urllib.parse import quote, urlparse

HOST = os.environ.get("PENGUPOOL_HOST", "")  # the workstation's Tailscale name; set it in the environment
PORT = int(os.environ.get("PENGUPOOL_PORT", "7787"))
HERE = os.path.dirname(os.path.abspath(__file__))


def load_token():
    tok = os.environ.get("PENGUPOOL_DEVICE_TOKEN", "").strip()
    if tok:
        return tok
    with open(os.path.join(HERE, "device-token")) as f:
        return f.read().strip()


class Sock:
    """Buffered socket reader."""

    def __init__(self, s):
        self.s = s
        self.buf = bytearray()

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


def connect():
    """A socket to the workstation: through $HTTPS_PROXY's CONNECT tunnel when set, else direct."""
    if not HOST:
        raise SystemExit("PENGUPOOL_HOST is not set: the workstation's Tailscale name")
    proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")
    if not proxy:
        sk = Sock(socket.create_connection((HOST, PORT), timeout=20))
    else:
        p = urlparse(proxy)
        # The Muse runtime proxy takes CONNECT on 3130, not on the port
        # in its own URL (that port closes CONNECT attempts): try the
        # URL's port first, then 3130.
        ports = [p.port or 3130]
        if ports[0] != 3130:
            ports.append(3130)
        sk, last = None, None
        for port in ports:
            try:
                sk = Sock(socket.create_connection((p.hostname, port), timeout=20))
                sk.s.sendall(f"CONNECT {HOST}:{PORT} HTTP/1.1\r\nHost: {HOST}:{PORT}\r\n\r\n".encode())
                status = sk.read_until(b"\r\n\r\n").decode("latin1").split("\r\n", 1)[0]
                if " 200 " not in status:
                    raise SystemExit(f"tunnel CONNECT failed: {status}")
                if sk.buf:
                    raise SystemExit("tunnel sent data before the request")
                break
            except (OSError, EOFError, SystemExit) as e:
                last = e
                if sk is not None:
                    try:
                        sk.s.close()
                    except Exception:
                        pass
                sk = None
        if sk is None:
            raise SystemExit(f"tunnel CONNECT failed: {last}")
    if PORT == 443:  # Serve's HTTPS: the token is encrypted to the workstation, not just to the proxy
        sk.s = ssl.create_default_context().wrap_socket(sk.s, server_hostname=HOST)
    return sk


def api(method, path, body=None):
    """One call on the device API. Returns (status, parsed JSON or None)."""
    sk = connect()
    data = json.dumps(body).encode() if body is not None else b""
    host = HOST if PORT == 443 else f"{HOST}:{PORT}"
    head = (f"{method} /api/muse/v1{path} HTTP/1.1\r\nHost: {host}\r\n"
            f"Authorization: Bearer {load_token()}\r\nConnection: close\r\n")
    if body is not None:
        head += f"Content-Type: application/json\r\nContent-Length: {len(data)}\r\n"
    sk.s.sendall((head + "\r\n").encode() + data)
    lines = sk.read_until(b"\r\n\r\n").decode("latin1").split("\r\n")
    status = int(lines[0].split(" ", 2)[1])
    headers = {k.lower(): v for k, _, v in (ln.partition(": ") for ln in lines[1:] if ": " in ln)}
    if "chunked" in headers.get("transfer-encoding", ""):
        raw = bytearray()
        while True:
            n = int(sk.read_until(b"\r\n").decode("latin1").split(";")[0], 16)
            if n == 0:
                break
            raw += sk.read_exact(n)
            sk.read_until(b"\r\n")
    else:
        n = int(headers.get("content-length", "0"))
        raw = sk.read_exact(n) if n else b""
    sk.s.close()
    try:
        return status, json.loads(raw) if raw else None
    except ValueError:
        return status, {"error": raw.decode("utf-8", "replace").strip()}


def healthz():
    """GET /api/healthz — no token; the gateway answers it on the network.

    Returns (status, parsed JSON or None). The response's epoch changes
    at every daemon start: it is the detector's restart signal now that
    settings.get (which carried the version) is local-only.
    """
    sk = connect()
    host = HOST if PORT == 443 else f"{HOST}:{PORT}"
    sk.s.sendall((f"GET /api/healthz HTTP/1.1\r\nHost: {host}\r\n"
                  "Connection: close\r\n\r\n").encode())
    lines = sk.read_until(b"\r\n\r\n").decode("latin1").split("\r\n")
    status = int(lines[0].split(" ", 2)[1])
    headers = {k.lower(): v for k, _, v in (ln.partition(": ") for ln in lines[1:] if ": " in ln)}
    if "chunked" in headers.get("transfer-encoding", ""):
        raw = bytearray()
        while True:
            n = int(sk.read_until(b"\r\n").decode("latin1").split(";")[0], 16)
            if n == 0:
                break
            raw += sk.read_exact(n)
            sk.read_until(b"\r\n")
    else:
        n = int(headers.get("content-length", "0"))
        raw = sk.read_exact(n) if n else b""
    sk.s.close()
    try:
        return status, json.loads(raw) if raw else None
    except ValueError:
        return status, None


def ok(status, res, what):
    if status >= 400:
        msg = (res or {}).get("error") or (res or {}).get("message") or res
        raise SystemExit(f"{what} failed ({status}): {msg}")
    return res


def cmd_sessions(_args):
    for s in ok(*api("GET", "/sessions"), "sessions") or []:
        print(json.dumps(s, ensure_ascii=False))


def cmd_next(_args):
    res = ok(*api("GET", "/next"), "next")
    if res:
        print(json.dumps(res, indent=2, ensure_ascii=False))


def cmd_answer(args):
    if len(args) < 3:
        raise SystemExit("usage: pp.py answer ASK_ID PAYLOAD_HASH ACTION [TEXT]")
    ask_id, payload_hash, action = args[:3]
    body = {"action": action, "payload_hash": payload_hash, "idempotency_key": str(uuid.uuid4())}
    if len(args) > 3:
        body["text"] = " ".join(args[3:])
    ok(*api("POST", f"/asks/{quote(ask_id, safe=':')}/answer", body), "answer")
    print(f"answered {ask_id}: {action}")


def cmd_say(args):
    if len(args) < 2:
        raise SystemExit("usage: pp.py say SESSION WORDS...")
    ok(*api("POST", f"/sessions/{quote(args[0], safe='')}/say", {"words": " ".join(args[1:])}), "say")
    print(f"sent to {args[0]}")


def cmd_action(args):
    if len(args) != 2:
        raise SystemExit("usage: pp.py action SESSION stop|interrupt|resume")
    res = ok(*api("POST", f"/sessions/{quote(args[0], safe='')}/action", {"action": args[1]}), "action")
    print(json.dumps(res, ensure_ascii=False))


def cmd_compass(_args):
    print(json.dumps(ok(*api("GET", "/compass"), "compass"), indent=2, ensure_ascii=False))


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in CMDS:
        print(__doc__)
        return 2
    CMDS[sys.argv[1]](sys.argv[2:])
    return 0


CMDS = {"sessions": cmd_sessions, "next": cmd_next, "answer": cmd_answer, "say": cmd_say,
        "action": cmd_action, "compass": cmd_compass}

if __name__ == "__main__":
    sys.exit(main())
