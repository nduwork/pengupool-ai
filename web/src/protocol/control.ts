// Control messages of docs/protocol/v1.md section 2, matching the JSON shape of
// internal/protocol/control.go field for field (one machine-readable schema for all traffic).
import { Codes, errorf } from "./errors";
import type { Domain } from "./domain";
import { FrameType, HEADER_SIZE, MAX_CONTROL_PAYLOAD, decodeControlPayload, type Frame } from "./frame";

/** Control message types. */
export const T = {
  Hello: "hello",
  HelloOK: "hello.ok",
  Ping: "ping",
  Pong: "pong",
  Error: "error",
  StateSubscribe: "state.subscribe",
  StateSnapshot: "state.snapshot",
  StateDelta: "state.delta",
  SessionAttach: "session.attach",
  SessionAttached: "session.attached",
  SessionDetach: "session.detach",
  SessionResize: "session.resize",
  SessionExit: "session.exit",
  SessionCreate: "session.create",
  SessionList: "session.list",
  SessionKill: "session.kill",
  SnapshotBegin: "snapshot.begin",
  SnapshotEnd: "snapshot.end",
  LeaseChanged: "lease.changed",
} as const;

/** Attach modes reported in session.attached. */
export const Mode = { Replay: "replay", Snapshot: "snapshot" } as const;

/** The protocol major version carried in the hello exchange. */
export const VERSION = 1;

/** Capabilities advertised by the daemon in v1, in the order internal/protocol/control.go lists them. */
export const CAPABILITIES = [
  T.StateSubscribe,
  T.SessionAttach,
  "session.input",
  T.SessionResize,
  "session.snapshot",
  T.SessionCreate,
  T.SessionList,
  T.SessionKill,
  "session.lease",
] as const;

export interface ClientInfo {
  kind: string;
  name?: string;
  version?: string;
}

/** The client state a reconnecting client still holds, so the server can choose replay vs snapshot. */
export interface RetainedState {
  generation: number;
  offset: number;
}

/** One session as the daemon describes it (internal/protocol.SessionView). */
export interface SessionView {
  id: string;
  name: string;
  node_id: string;
  cwd: string;
  command?: string[];
  harness?: string;
  /** The launcher it runs through: "ollama" for Claude Code (Ollama). */
  via?: string;
  state: string;
  exit_code?: number;
  pid?: number;
  cols: number;
  rows: number;
  started_at?: string;
  status?: string;
  /** The first-run dialog the agent is stuck on while starting. */
  waiting?: string;
  /** What the agent asks the owner now (a permission, a question, its plan); answered in its terminal. */
  question?: string;
  /** Prompted 2+ minutes ago, and none of PenguPool's hooks has arrived since the session launched. */
  hooks_missing?: boolean;
  team_id?: string;
  role?: string;
  skill_id?: string;
  context_pct?: number;
  branch?: string;
  run_log?: string;
  active_at?: string;
  summary?: string;
}

/**
 * One control message. Every message carries id and type; the rest are per-type and omitted when
 * empty, which is what keeps a single schema workable for all of them.
 */
export interface ControlMessage {
  id: string;
  type: string;
  v?: number;
  client?: ClientInfo;
  node_id?: string;
  connection_id?: string;
  capabilities?: string[];

  code?: string;
  message?: string;

  since_rev?: number;
  epoch?: string;
  rev?: number;
  sessions?: SessionView[];
  upserted?: SessionView[];
  removed?: string[];

  session_id?: string;
  channel?: number;
  generation?: number;
  retained?: RetainedState;
  claim_lease?: boolean;
  /** This client resets before applying a snapshot, so every snapshot carries the history. */
  resets?: boolean;
  cols?: number;
  rows?: number;
  mode?: string;
  offset?: number;
  /** Kind of the client behind a relayed request (session.attach): "cli" | "browser" | "ios". */
  kind?: string;

  /** lease.changed: true in the holder's own copy, naming this client as the size owner. */
  owner?: boolean;

  name?: string;
  cwd?: string;
  command?: string[];
  harness?: string;

  exit_code?: number;
  at?: string;

  snapshot_id?: string;
  parts?: number;

  /** Pool state, whole, in state.snapshot and in a state.delta when it changed. */
  domain?: Domain;
  /** Pool request arguments and results. */
  params?: unknown;
  result?: unknown;
}

/** Decodes a control frame into a message, refusing anything without an id and a type. */
export function decodeControl(f: Frame): ControlMessage {
  if (f.header.type !== FrameType.Control) {
    throw errorf(
      Codes.InvalidArgument,
      `expected a control frame, got ${f.header.type}`,
    );
  }
  const text = decodeControlPayload(f.payload);
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw errorf(Codes.InvalidArgument, `control payload: ${(err as Error).message}`);
  }
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    throw errorf(Codes.InvalidArgument, "control payload is not a JSON object");
  }
  const m = raw as ControlMessage;
  if (typeof m.id !== "string" || m.id === "") {
    throw errorf(Codes.InvalidArgument, "control message has no id");
  }
  if (typeof m.type !== "string" || m.type === "") {
    throw errorf(Codes.InvalidArgument, "control message has no type");
  }
  return m;
}

/**
 * Encodes a control message as a channel-0 control frame. Rejects a payload above the 1 MiB
 * control limit before it ever reaches the socket.
 */
export function encodeControl(m: ControlMessage): Uint8Array {
  const payload = new TextEncoder().encode(JSON.stringify(m));
  return encodeControlPayload(payload);
}

/** Wraps an already-encoded JSON payload in a control header, refusing an over-limit payload. */
export function encodeControlPayload(payload: Uint8Array): Uint8Array {
  if (payload.length > MAX_CONTROL_PAYLOAD) {
    throw errorf(
      Codes.FrameTooLarge,
      `control message is ${payload.length} bytes, limit is ${MAX_CONTROL_PAYLOAD}`,
    );
  }
  const out = new Uint8Array(HEADER_SIZE + payload.length);
  const dv = new DataView(out.buffer);
  dv.setUint8(0, FrameType.Control);
  dv.setUint32(1, 0);
  dv.setBigUint64(5, 0n);
  dv.setUint32(13, payload.length);
  out.set(payload, HEADER_SIZE);
  return out;
}

/** True when a message is an error response; the caller switches on `code`. */
export function isError(m: ControlMessage): boolean {
  return m.type === T.Error;
}
