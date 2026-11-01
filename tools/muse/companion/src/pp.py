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

The device token is read from $PENGUPOOL_DEVICE_TOKEN or ./device-token
(mode 0600). The bootstrap token is never read, held, or sent by this
module.

PENGUPOOL_HOST is required: the workstation's tailnet name or IP, or the
pairing URL the app shows (its scheme then picks serve or serve-http
unless PENGUPOOL_TRANSPORT is set). Calls
fail fast when it is unset, so a missing config can never send the
device token to a baked-in address. PENGUPOOL_HOST_HEADER defaults from
PENGUPOOL_HOST; set it to the tailnet name when the host is an IP, since
it drives the TLS SNI, the Host header, and the certificate check.

Transports (PENGUPOOL_TRANSPORT):
  serve  (default) — the v0.10.20 path. The daemon binds loopback only
    ("Local network was removed ... use Tailscale Serve"), so the device
    API is reached through Tailscale Serve: CONNECT to the workstation's
    tailnet address on port 443 via the runtime proxy, TLS with SNI =
    the machine's tailnet name (Serve's certificate is publicly trusted
    for that name), then plain HTTP/1.1 with Host = the tailnet name.
  serve-http — Tailscale Serve in plain-HTTP mode (--http=80), for
    tailnets where HTTPS certificates are not enabled. CONNECT to the
    tailnet address on port 80 and speak plaintext HTTP with
    Host = the tailnet name. The tailnet itself is WireGuard-encrypted,
    and the device token remains the credential.
  direct — the pre-0.10.20 path: CONNECT to the tailnet address on the
    daemon port (7787) and speak plaintext HTTP with
    Host = <tailnet name>:<port>. Only useful while a daemon build
    still binds a network listener (and for the self-test's local
    server).
PENGUPOOL_CA_FILE overrides the trust roots (tests, private CA).
"""
import json
import os
import socket
import ssl
import sys
import uuid
from urllib.parse import quote, urlparse



def resolve_host(host, transport):
    """Host and transport from PENGUPOOL_HOST and PENGUPOOL_TRANSPORT.

    PENGUPOOL_HOST may be the pairing URL the app shows
    (scheme://<name>/api/muse/v1): its scheme picks the transport
    (https -> serve, http -> serve-http) unless PENGUPOOL_TRANSPORT is set.
    """
    if "://" in host:
        u = urlparse(host)
        host = u.hostname or ""
        transport = transport or ("serve-http" if u.scheme == "http" else "serve")
    return host, transport or "serve"


HOST, TRANSPORT = resolve_host(os.environ.get("PENGUPOOL_HOST", ""), os.environ.get("PENGUPOOL_TRANSPORT", ""))
HOST_HEADER = os.environ.get("PENGUPOOL_HOST_HEADER", "") or HOST
PORT = int(os.environ.get("PENGUPOOL_PORT", "7787"))
SERVE_PORT = int(os.environ.get("PENGUPOOL_SERVE_PORT", "443"))
SERVE_HTTP_PORT = int(os.environ.get("PENGUPOOL_SERVE_HTTP_PORT", "80"))
HERE = os.path.dirname(os.path.abspath(__file__))
API = "/api/muse/v1"


def load_token():
    tok = os.environ.get("PENGUPOOL_DEVICE_TOKEN", "").strip()
    if tok:
        return tok
    with open(os.path.join(HERE, "device-token")) as f:
        return f.read().strip()


load_device_token = load_token  # the v0.10.20 name


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
    """CONNECT through the runtime proxy to host:port, else direct.

    Returns (raw_socket, reader) where reader has consumed exactly the
    CONNECT response headers (direct: a fresh reader on the socket).
    Raises EOFError if the proxy or the target closes the connection —
    that is the companion's "unreachable" signal.
    """
    proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")
    if not proxy:
        raw = socket.create_connection((host, port), timeout=20)
        return raw, Sock(raw)
    p = urlparse(proxy)
    # The Muse runtime proxy takes CONNECT on 3130, not on the port
    # in its own URL (that port closes CONNECT attempts): try the
    # URL's port first, then 3130.
    ports = [p.port or 3130]
    if ports[0] != 3130:
        ports.append(3130)
    sk, errs = None, []
    for cport in ports:
        # A refusal is the proxy's answer: same host, so no other port answers
        # differently — fail fast. Only a failed connection tries the next port.
        try:
            raw = socket.create_connection((p.hostname, cport), timeout=20)
            sk = Sock(raw)
            sk.s.sendall((f"CONNECT {host}:{port} HTTP/1.1\r\n"
                          f"Host: {host}:{port}\r\nProxy-Connection: keep-alive\r\n\r\n").encode())
            status = sk.read_until(b"\r\n\r\n").decode("latin1").split("\r\n", 1)[0]
        except (OSError, EOFError) as e:
            errs.append(f"{cport}: {e}")
            if sk is not None:
                try:
                    sk.s.close()
                except Exception:
                    pass
            sk = None
            continue
        if " 200 " not in status:
            sk.s.close()
            raise SystemExit(f"tunnel CONNECT failed: {status}")
        return sk.s, sk
    raise SystemExit(f"tunnel CONNECT failed: {'; '.join(errs)}")


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


def _tunnel_serve_http():
    """Serve in plain-HTTP mode: no TLS wrap, so the proxy reader (and
    any bytes it buffered) is used as-is."""
    _, sk = _connect_proxy(HOST, SERVE_HTTP_PORT)
    return sk


def _tunnel_direct():
    _, sk = _connect_proxy(HOST, PORT)
    return sk


def tunnel():
    if not HOST:
        raise SystemExit("PENGUPOOL_HOST is not set: the workstation's Tailscale name or IP")
    if TRANSPORT == "direct":
        return _tunnel_direct()
    if TRANSPORT == "serve-http":
        return _tunnel_serve_http()
    if TRANSPORT == "serve":
        return _tunnel_serve()
    raise SystemExit(f"unknown PENGUPOOL_TRANSPORT: {TRANSPORT!r}")


def connect():
    """A socket to the workstation: the pre-0.10.20 name for tunnel()."""
    return tunnel()


def _request(method, path, body=None, auth=True):
    """One HTTP request to the daemon over the tunnel. Returns (status, body bytes)."""
    sk = tunnel()
    payload = b""
    host_header = HOST_HEADER if TRANSPORT in ("serve", "serve-http") else f"{HOST_HEADER}:{PORT}"
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


def api(method, path, body=None):
    """One call on the device API. Returns (status, parsed JSON or None)."""
    status, raw = _request(method, API + path, body=body)
    try:
        return status, json.loads(raw) if raw else None
    except ValueError:
        return status, {"error": raw.decode("utf-8", "replace").strip()}


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


def ok(status, res, what):
    if status >= 400:
        msg = (res or {}).get("error") or (res or {}).get("message") or res
        raise SystemExit(f"{what} failed ({status}): {msg}")
    return res


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
        cmd_say(args)
    elif cmd == "answer":
        cmd_answer(args)
    elif cmd == "action":
        cmd_action(args)
    elif cmd == "compass":
        cmd_compass(args)
    else:
        print(f"unknown command: {cmd}\n{__doc__}")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
