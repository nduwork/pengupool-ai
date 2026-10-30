// One WebSocket to the daemon's gateway, carrying the binary v1 frames 1:1.
//
// The connection owns the reconnect loop, the hello handshake, the bootstrap token and the
// terminal-query ping/pong. It is deliberately dumb about terminals: frames are handed to whoever
// registered the frame's channel, control messages to whoever subscribed.
import { FrameDecoder, FrameType, encodeFrame, type Frame, type Header } from "../protocol/frame";
import { T, VERSION, encodeControl, decodeControl, type ControlMessage } from "../protocol/control";
import { Codes, isProtocolError } from "../protocol/errors";
import { newID } from "../protocol/uuid";

export type ConnectionStatus =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "unauthorized"
  | "incompatible"
  | "closed";

export interface ConnectionInfo {
  status: ConnectionStatus;
  /** Human-readable reason shown next to the status dot; never empty when the status is bad. */
  detail: string;
  nodeID: string;
  connectionID: string;
  capabilities: string[];
}

export interface ConnectionOptions {
  /** Path of the WebSocket endpoint; the bootstrap token is appended as `?token=`. */
  path?: string;
  /** The daemon's bootstrap token from `<store>/run/hub.token`, if the page was given one. */
  token?: string;
  /** The host's HTTP origin (`http://127.0.0.1:7787`) when the page is not served by the host. */
  baseURL?: string;
  /**
   * The kind announced in `hello`. `ios` marks a view-only phone (docs/protocol/v1.md section 4):
   * such a client never claims the size lease and never sends input. Defaults to a platform probe.
   */
  clientKind?: string;
  clientName?: string;
  clientVersion?: string;
  /** Backoff floor; tests override it. */
  baseDelayMs?: number;
  maxDelayMs?: number;
}

const INITIAL: ConnectionInfo = {
  status: "connecting",
  detail: "connecting",
  nodeID: "",
  connectionID: "",
  capabilities: [],
};

type StatusListener = (info: ConnectionInfo) => void;
type ControlListener = (m: ControlMessage) => void;
type FrameListener = (f: Frame) => void;

function errorCode(err: unknown): string {
  return isProtocolError(err) ? err.code : Codes.InvalidArgument;
}

export class Connection {
  /** The kind this connection announces in `hello`; the pane treats `ios` as view-only. */
  readonly clientKind: string;
  private readonly opts: Required<ConnectionOptions>;
  private ws: WebSocket | undefined;
  private decoder = new FrameDecoder();
  private readonly frameHandlers = new Map<number, FrameListener>();
  private readonly controlListeners = new Set<ControlListener>();
  private readonly frameListeners = new Set<FrameListener>();
  private readonly statusListeners = new Set<StatusListener>();
  private info: ConnectionInfo = INITIAL;
  private attempt = 0;
  private timer: number | undefined;
  private stopped = false;
  /** Bumped whenever a socket is superseded, so a stale socket's events are ignored. */
  private gen = 0;

  constructor(opts: ConnectionOptions = {}) {
    this.opts = {
      path: opts.path ?? "/ws",
      token: opts.token ?? "",
      baseURL: opts.baseURL ?? "",
      clientKind: opts.clientKind ?? "browser",
      clientName: opts.clientName ?? "pengupool web",
      clientVersion: opts.clientVersion ?? "0.1.0-prototype",
      baseDelayMs: opts.baseDelayMs ?? 500,
      maxDelayMs: opts.maxDelayMs ?? 10_000,
    };
    this.clientKind = this.opts.clientKind;
  }

  get status(): ConnectionInfo {
    return this.info;
  }

  onStatus(fn: StatusListener): () => void {
    this.statusListeners.add(fn);
    fn(this.info);
    return () => this.statusListeners.delete(fn);
  }

  onControl(fn: ControlListener): () => void {
    this.controlListeners.add(fn);
    return () => this.controlListeners.delete(fn);
  }

  onFrame(fn: FrameListener): () => void {
    this.frameListeners.add(fn);
    return () => this.frameListeners.delete(fn);
  }

  /** Routes frames on one terminal channel (every attached session owns a distinct one). */
  registerChannel(channel: number, fn: FrameListener): () => void {
    this.frameHandlers.set(channel, fn);
    return () => {
      if (this.frameHandlers.get(channel) === fn) this.frameHandlers.delete(channel);
    };
  }

  /** Opens the socket, probing the token first so a bad token is not mistaken for a flaky network. */
  async start(): Promise<void> {
    this.stopped = false;
    if (await this.probeUnauthorized()) {
      this.setInfo({
        status: "unauthorized",
        detail: "not authorized: the daemon's bootstrap token is missing or wrong",
        nodeID: "",
        connectionID: "",
        capabilities: [],
      });
      return;
    }
    this.open();
  }

  stop(): void {
    this.stopped = true;
    this.gen++;
    window.clearTimeout(this.timer);
    this.timer = undefined;
    this.ws?.close(1000, "client closed");
    this.ws = undefined;
  }

  /** Sends a control message on channel 0, assigning the request id. Returns false if offline. */
  send(msg: Omit<ControlMessage, "id">): boolean {
    if (this.ws === undefined || this.ws.readyState !== WebSocket.OPEN) return false;
    this.ws.send(encodeControl({ ...msg, id: newID() }));
    return true;
  }

  /**
   * Sends a pool request and resolves with its result, or rejects with the daemon's error. Pool
   * requests never wait offline: a disconnected page cannot mutate anything.
   */
  request<R = unknown>(type: string, params: unknown = {}, timeoutMs = 120_000): Promise<R> {
    return new Promise<R>((resolve, reject) => {
      if (this.ws === undefined || this.ws.readyState !== WebSocket.OPEN) {
        reject(Object.assign(new Error("not connected to the daemon"), { code: "unavailable" }));
        return;
      }
      const id = newID();
      const off = (): void => {
        this.controlListeners.delete(listener);
        window.clearTimeout(timer);
      };
      const listener: ControlListener = (m) => {
        if (m.id !== id) return;
        off();
        if (m.type === T.Error) reject(Object.assign(new Error(m.message ?? m.code ?? "error"), { code: m.code }));
        else resolve(m.result as R);
      };
      const timer = window.setTimeout(() => {
        off();
        reject(Object.assign(new Error(`${type} timed out`), { code: "unavailable" }));
      }, timeoutMs);
      this.controlListeners.add(listener);
      this.ws.send(encodeControl({ id, type, params }));
    });
  }

  /** The bootstrap token, for the bounded HTTP reads (files, changes, diffs). */
  get token(): string {
    return this.opts.token;
  }

  /** The host's HTTP origin, for the bounded HTTP reads. */
  get baseURL(): string {
    return this.opts.baseURL !== "" ? this.opts.baseURL : location.origin;
  }

  /** Points the connection at a (restarted) host and reconnects. */
  retarget(baseURL: string, token: string): void {
    this.opts.baseURL = baseURL;
    this.opts.token = token;
    this.stop();
    this.attempt = 0;
    void this.start();
  }

  /** Sends one already-framed data frame. Returns false if offline. */
  sendFrame(header: Header, payload: Uint8Array): boolean {
    if (this.ws === undefined || this.ws.readyState !== WebSocket.OPEN) return false;
    this.ws.send(encodeFrame({ header, payload }));
    return true;
  }

  private setInfo(info: ConnectionInfo): void {
    this.info = info;
    for (const fn of this.statusListeners) fn(info);
  }

  private wsURL(): string {
    const base = new URL(this.baseURL);
    const scheme = base.protocol === "https:" ? "wss:" : "ws:";
    const u = new URL(this.opts.path, `${scheme}//${base.host}`);
    if (this.opts.token !== "") u.searchParams.set("token", this.opts.token);
    return u.toString();
  }

  /**
   * A WebSocket handshake failure reaches script as a bare 1006, so a 401 is invisible unless we
   * ask over HTTP first. Only a 401 is read out of this; a non-upgrade GET (400/426) or a network
   * error merely means "carry on and try the upgrade".
   */
  private async probeUnauthorized(): Promise<boolean> {
    const url = new URL(this.wsURL());
    url.protocol = url.protocol === "wss:" ? "https:" : "http:";
    const abort = new AbortController();
    const timeout = window.setTimeout(() => abort.abort(), 3000);
    try {
      const res = await fetch(url.toString(), { method: "GET", signal: abort.signal });
      return res.status === 401;
    } catch {
      return false;
    } finally {
      window.clearTimeout(timeout);
    }
  }

  private open(): void {
    const gen = ++this.gen;
    this.setInfo({
      ...this.info,
      status: this.attempt === 0 ? "connecting" : "reconnecting",
      detail: this.attempt === 0 ? "connecting" : `reconnecting (attempt ${this.attempt})`,
    });
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.wsURL());
    } catch (err) {
      this.scheduleReconnect(String(err));
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    this.decoder = new FrameDecoder();

    const live = (): boolean => gen === this.gen && !this.stopped;
    ws.addEventListener("open", () => {
      if (!live()) return;
      this.send({
        type: T.Hello,
        v: VERSION,
        client: { kind: this.clientKind, name: this.opts.clientName, version: this.opts.clientVersion },
      });
    });
    ws.addEventListener("message", (ev) => {
      if (live()) this.onMessage(ev);
    });
    ws.addEventListener("error", () => {
      /* the close event that follows carries the real story */
    });
    ws.addEventListener("close", (ev) => {
      if (gen !== this.gen) return;
      this.ws = undefined;
      if (this.stopped) {
        this.setInfo({ ...this.info, status: "closed", detail: "connection closed" });
        return;
      }
      this.scheduleReconnect(ev.reason !== "" ? ev.reason : `socket closed (${ev.code})`);
    });
  }

  private onMessage(ev: MessageEvent): void {
    if (typeof ev.data === "string") {
      this.fatal(Codes.Unsupported, "received a text frame; the protocol is binary-only");
      return;
    }
    // WebSocket frames arrive as ArrayBuffer because binaryType was set on open.
    const bytes = new Uint8Array(ev.data as ArrayBuffer);
    let frames: Frame[];
    try {
      frames = this.decoder.push(bytes);
    } catch (err) {
      this.fatal(errorCode(err), (err as Error).message);
      return;
    }
    for (const frame of frames) {
      if (frame.header.type !== FrameType.Control) {
        this.frameHandlers.get(frame.header.channel)?.(frame);
        for (const fn of this.frameListeners) fn(frame);
        continue;
      }
      let m: ControlMessage;
      try {
        m = decodeControl(frame);
      } catch (err) {
        this.fatal(errorCode(err), (err as Error).message);
        return;
      }
      this.dispatchControl(m);
    }
  }

  private dispatchControl(m: ControlMessage): void {
    if (m.type === T.Ping) {
      this.send({ type: T.Pong });
      return;
    }
    if (m.type === T.HelloOK) {
      if (m.v !== VERSION) {
        this.refuse(`daemon speaks v${m.v ?? 0}, this client speaks v${VERSION}`);
        return;
      }
      this.attempt = 0;
      this.setInfo({
        status: "connected",
        detail: `connected to ${m.node_id ?? "daemon"}`,
        nodeID: m.node_id ?? "",
        connectionID: m.connection_id ?? "",
        capabilities: m.capabilities ?? [],
      });
      return;
    }
    if (m.type === T.Error && m.code === Codes.IncompatibleVersion) {
      this.refuse(m.message ?? `daemon refused v${VERSION}`);
      return;
    }
    for (const fn of this.controlListeners) fn(m);
  }

  /** Stops for good on a version mismatch: retrying cannot fix it, so it must be visible. */
  private refuse(detail: string): void {
    this.setInfo({
      status: "incompatible",
      detail: `incompatible version: ${detail}`,
      nodeID: this.info.nodeID,
      connectionID: this.info.connectionID,
      capabilities: this.info.capabilities,
    });
    this.stopped = true;
    this.gen++;
    this.ws?.close(1002, "incompatible version");
    this.ws = undefined;
  }

  private scheduleReconnect(reason: string): void {
    if (this.stopped) return;
    this.attempt++;
    const capped = Math.min(this.opts.baseDelayMs * 2 ** (this.attempt - 1), this.opts.maxDelayMs);
    const delay = Math.round(capped * (0.75 + Math.random() * 0.5));
    this.setInfo({
      ...this.info,
      status: "reconnecting",
      detail: `reconnecting in ${(delay / 1000).toFixed(1)}s (${reason})`,
    });
    this.timer = window.setTimeout(() => {
      this.timer = undefined;
      void this.reconnect();
    }, delay);
  }

  private async reconnect(): Promise<void> {
    if (this.stopped) return;
    if (await this.probeUnauthorized()) {
      this.gen++;
      this.setInfo({
        status: "unauthorized",
        detail: "not authorized: the daemon's bootstrap token is missing or wrong",
        nodeID: "",
        connectionID: "",
        capabilities: [],
      });
      return;
    }
    this.open();
  }

  /** A malformed stream is fatal for this socket only: drop it and let the backoff retry. */
  private fatal(code: string, message: string): void {
    const ws = this.ws;
    this.ws = undefined;
    this.gen++;
    if (ws !== undefined && ws.readyState < WebSocket.CLOSING) ws.close(1002, code.slice(0, 100));
    this.scheduleReconnect(`${code}: ${message}`);
  }
}
