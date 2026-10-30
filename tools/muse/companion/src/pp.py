#!/usr/bin/env python3
"""PenguPool owner-API client.

Speaks the gateway /ws protocol (protocol v1: 17-byte-headed frames, one
frame per binary WebSocket message) over the Tailscale tunnel proxy.

Usage:
  pp.py call TYPE [JSON]        one pool RPC, prints the result JSON
  pp.py sessions                session.list
  pp.py state                   state.subscribe snapshot (sessions + domain)
  pp.py screen SESSION          attach, read the session's screen, print it
  pp.py history SESSION [N]     history.read RPC (user/assistant text)
  pp.py say SESSION WORDS...    attach + type the words + Enter (owner input)

The daemon bootstrap token is read from ./token (mode 0600) or $PENGUPOOL_TOKEN.
"""
import base64
import hashlib
import json
import os
import socket
import struct
import sys
import time
import uuid
from urllib.parse import urlparse

HOST = os.environ.get("PENGUPOOL_HOST", "")  # the workstation's tailnet name or IP; set it in the environment
PORT = int(os.environ.get("PENGUPOOL_PORT", "7787"))
HERE = os.path.dirname(os.path.abspath(__file__))

FT_CONTROL, FT_OUTPUT, FT_INPUT, FT_ACK, FT_SNAPSHOT = 0x01, 0x02, 0x03, 0x04, 0x05


def load_token():
    tok = os.environ.get("PENGUPOOL_TOKEN", "").strip()
    if tok:
        return tok
    with open(os.path.join(HERE, "token")) as f:
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


def tunnel():
    if not HOST:
        raise SystemExit("PENGUPOOL_HOST is not set: the workstation's tailnet name or IP")
    proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")
    if not proxy:
        raise SystemExit("HTTPS_PROXY is not set; cannot reach the tunnel proxy")
    p = urlparse(proxy)
    raw = socket.create_connection((p.hostname, p.port or 3130), timeout=20)
    req = (
        f"CONNECT {HOST}:{PORT} HTTP/1.1\r\n"
        f"Host: {HOST}:{PORT}\r\nProxy-Connection: keep-alive\r\n\r\n"
    )
    raw.sendall(req.encode())
    sk = Sock(raw)
    head = sk.read_until(b"\r\n\r\n").decode("latin1")
    status = head.split("\r\n", 1)[0]
    if " 200 " not in status:
        raise SystemExit(f"tunnel CONNECT failed: {status}")
    return sk


class Client:
    def __init__(self):
        self.sk = tunnel()
        self.token = load_token()
        self._ws_handshake()
        self.buf = bytearray()
        self.seq = 0

    # ---- WebSocket layer (client-masked, binary) ----
    def _ws_handshake(self):
        key = base64.b64encode(os.urandom(16)).decode()
        req = (
            f"GET /ws?token={self.token} HTTP/1.1\r\n"
            f"Host: {HOST}:{PORT}\r\n"
            "Upgrade: websocket\r\nConnection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
        )
        self.sk.s.sendall(req.encode())
        head = self.sk.read_until(b"\r\n\r\n").decode("latin1")
        status = head.split("\r\n", 1)[0]
        if " 101 " not in status:
            raise SystemExit(f"websocket upgrade failed: {status} {head[:200]}")
        accept = base64.b64encode(
            hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()
        ).decode()
        if accept not in head:
            raise SystemExit("websocket accept key mismatch")

    def _ws_send(self, payload, opcode=0x2):
        mask = os.urandom(4)
        n = len(payload)
        hdr = bytearray([0x80 | opcode])
        if n < 126:
            hdr.append(0x80 | n)
        elif n <= 0xFFFF:
            hdr.append(0x80 | 126)
            hdr += struct.pack(">H", n)
        else:
            hdr.append(0x80 | 127)
            hdr += struct.pack(">Q", n)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self.sk.s.sendall(bytes(hdr) + mask + masked)

    def _ws_recv_msg(self):
        frag = bytearray()
        while True:
            b1, b2 = self.sk.read_exact(2)
            fin, opcode = b1 >> 7, b1 & 0x0F
            ln = b2 & 0x7F
            if ln == 126:
                ln = struct.unpack(">H", self.sk.read_exact(2))[0]
            elif ln == 127:
                ln = struct.unpack(">Q", self.sk.read_exact(8))[0]
            data = self.sk.read_exact(ln) if ln else b""
            if opcode == 0x9:  # ping -> pong
                self._ws_send(data, opcode=0xA)
                continue
            if opcode == 0x8:
                raise EOFError("websocket closed by server")
            frag += data
            if fin:
                return bytes(frag)

    # ---- protocol v1 frame layer ----
    def send_frame(self, ftype, channel, payload):
        hdr = struct.pack(">BIQI", ftype, channel, 0, len(payload))
        self._ws_send(hdr + payload)

    def send_control(self, msg):
        if "id" not in msg:
            msg["id"] = str(uuid.uuid4())
        self.send_frame(FT_CONTROL, 0, json.dumps(msg).encode())
        return msg["id"]

    def read_frame(self, timeout=None):
        if timeout is not None:
            self.sk.s.settimeout(timeout)
        while True:
            if len(self.buf) >= 17:
                ftype = self.buf[0]
                channel, _seq, ln = struct.unpack(">IQI", bytes(self.buf[1:17]))
                if len(self.buf) >= 17 + ln:
                    payload = bytes(self.buf[17 : 17 + ln])
                    del self.buf[: 17 + ln]
                    return ftype, channel, payload
            self.buf += self._ws_recv_msg()

    # ---- conversation ----
    def hello(self):
        mid = self.send_control(
            {"type": "hello", "v": 1, "client": {"kind": "browser", "name": "Muse (agent)"}}
        )
        while True:
            ftype, _ch, payload = self.read_frame()
            if ftype != FT_CONTROL:
                continue
            m = json.loads(payload)
            if m.get("id") == mid:
                if m.get("type") == "error":
                    raise SystemExit(f"hello refused: {m.get('code')}: {m.get('message')}")
                return m

    def call(self, typ, params=None, timeout=60):
        msg = {"type": typ}
        if params is not None:
            msg["params"] = params
        mid = self.send_control(msg)
        self.sk.s.settimeout(timeout)
        while True:
            ftype, _ch, payload = self.read_frame()
            if ftype != FT_CONTROL:
                continue
            m = json.loads(payload)
            if m.get("id") != mid:
                continue
            if m.get("type") == "error":
                raise SystemExit(f"{typ} failed: {m.get('code')}: {m.get('message')}")
            return m

    def close(self):
        try:
            self._ws_send(b"", opcode=0x8)
        except Exception:
            pass
        try:
            self.sk.s.close()
        except Exception:
            pass


def cmd_call(c, args):
    typ = args[0]
    params = json.loads(args[1]) if len(args) > 1 else None
    m = c.call(typ, params)
    out = m.get("result", m)
    print(json.dumps(out, indent=2, ensure_ascii=False))


def cmd_sessions(c, _args):
    m = c.call("session.list")
    for s in m.get("sessions", []):
        print(json.dumps(s, ensure_ascii=False))


def cmd_state(c, _args):
    mid = c.send_control({"type": "state.subscribe"})
    deadline = time.time() + 20
    while time.time() < deadline:
        ftype, _ch, payload = c.read_frame(timeout=20)
        if ftype != FT_CONTROL:
            continue
        m = json.loads(payload)
        if m.get("type") == "state.snapshot":
            print(json.dumps({"sessions": m.get("sessions", []), "domain": m.get("domain", {})},
                             indent=2, ensure_ascii=False))
            return
    raise SystemExit("no state snapshot received")


def drain_attach(c, seconds=2.0):
    """Collect frames after an attach: control messages + raw stream bytes."""
    controls, stream = [], bytearray()
    end = time.time() + seconds
    c.sk.s.settimeout(0.5)
    while time.time() < end:
        try:
            ftype, _ch, payload = c.read_frame(timeout=0.5)
        except (socket.timeout, TimeoutError):
            continue
        if ftype == FT_CONTROL:
            controls.append(json.loads(payload))
        elif ftype in (FT_OUTPUT, FT_SNAPSHOT):
            stream += payload
    c.sk.s.settimeout(60)
    return controls, bytes(stream)


def render_screen(stream):
    """Best-effort screen text: pyte when available, else ANSI-stripped tail."""
    try:
        import pyte

        scr = pyte.Screen(200, 200)
        st = pyte.ByteStream(scr)
        st.feed(stream)
        lines = [ln.rstrip() for ln in scr.display]
        while lines and not lines[-1]:
            lines.pop()
        return "\n".join(lines)
    except ImportError:
        import re

        text = stream.decode("utf-8", "replace")
        text = re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]", "", text)
        text = re.sub(r"\x1b\][^\x07]*\x07", "", text)
        text = text.replace("\r", "\n")
        lines = [ln for ln in text.split("\n")]
        return "\n".join(lines[-60:])


def cmd_screen(c, args):
    sid = args[0]
    mid = c.send_control({"type": "session.attach", "session_id": sid, "channel": 7})
    controls, stream = drain_attach(c, seconds=3.0)
    attached = any(m.get("type") == "session.attached" for m in controls)
    errs = [m for m in controls if m.get("type") == "error"]
    if errs and not attached:
        raise SystemExit(f"attach failed: {errs[0].get('code')}: {errs[0].get('message')}")
    print(render_screen(stream))
    c.send_control({"type": "session.detach"})


def cmd_history(c, args):
    sid = args[0]
    limit = int(args[1]) if len(args) > 1 else 30
    m = c.call("history.read", {"session_id": sid, "limit": limit})
    res = m.get("result", {})
    if res.get("note"):
        print(f"({res['note']})")
    for it in res.get("items", []):
        print(f"[{it.get('role','?')}] {it.get('text','')}")


def cmd_say(c, args):
    sid, words = args[0], " ".join(args[1:])
    mid = c.send_control({"type": "session.attach", "session_id": sid, "channel": 7})
    controls, _stream = drain_attach(c, seconds=1.5)
    errs = [m for m in controls if m.get("type") == "error"]
    if errs and not any(m.get("type") == "session.attached" for m in controls):
        raise SystemExit(f"attach failed: {errs[0].get('code')}: {errs[0].get('message')}")
    # Text and Enter go as separate frames: one big chunk is treated as a
    # paste by the agent's TUI, and a pasted carriage return never submits.
    c.send_frame(FT_INPUT, 7, words.encode())
    time.sleep(0.6)
    c.send_frame(FT_INPUT, 7, b"\r")
    time.sleep(0.3)
    c.send_control({"type": "session.detach"})
    print(f"sent to {sid}: {words}")


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cmd, args = sys.argv[1], sys.argv[2:]
    c = Client()
    try:
        c.hello()
        {"call": cmd_call, "sessions": cmd_sessions, "state": cmd_state,
         "screen": cmd_screen, "history": cmd_history, "say": cmd_say}[cmd](c, args)
    finally:
        c.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
