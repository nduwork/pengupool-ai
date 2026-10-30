// Per-session attach state machine: replay vs snapshot, the size lease, input, acks.
//
// Rules this file exists to enforce (docs/protocol/v1.md sections 3-5):
//   * a snapshot is applied only after `snapshot.end`, and is discarded whole if parts are missing;
//     the worker sends snapshots on its own (a generation change, or this client falling out of the
//     ring), so `snapshot.begin` is accepted in every live state and output that arrives while the
//     snapshot is open is held until `snapshot.end`;
//   * the size lease is never claimed on attach - only on explicit focus or first input - so the
//     CLI attach client keeps it and the PTY does not see a SIGWINCH storm; a client whose kind is
//     `ios` is view-only and never claims it at all;
//   * a resync detaches before it attaches again, because the worker answers a second
//     `session.attach` on a live attachment with `conflict`;
//   * input is best-effort with seq 0 and is never resent across a reconnect;
//   * `lease.changed` tracks who owns the size: on losing it the browser stops answering
//     terminal queries and stops sending resizes, and both resume when it regains it;
//   * xterm.js's own terminal-query replies are dropped while another client holds the lease.
import { FrameType, type Frame } from "../protocol/frame";
import { T, Mode, type ControlMessage } from "../protocol/control";
import { Codes } from "../protocol/errors";
import { TerminalQueryFilter } from "../protocol/queries";

export type AttachState = "detached" | "attaching" | "replay" | "snapshot" | "live" | "exited";

/** The terminal pane side of the attach, so this state machine stays free of DOM and xterm.js. */
export interface TerminalSink {
  write(data: Uint8Array): void;
  /** Drops scrollback and screen state before a freshly captured snapshot is applied. */
  reset(): void;
}

export interface AttachEvents {
  state(state: AttachState, detail: string): void;
  /** A control error addressed to this session, e.g. `forbidden` from a lost lease. */
  error(code: string, message: string): void;
  exit(code: number | undefined, at: string | undefined): void;
}

export interface AttachOptions {
  sessionId: string;
  channel: number;
  /** The kind announced in `hello`; an `ios` client is view-only and never claims the lease. */
  kind?: string;
  sink: TerminalSink;
  send: (m: Omit<ControlMessage, "id">) => boolean;
  sendFrame: (type: FrameType, seq: bigint, payload: Uint8Array) => boolean;
  events: AttachEvents;
}

interface PendingSnapshot {
  id: string;
  generation: number;
  offset: number;
  parts: Uint8Array[];
}

function concat(parts: Uint8Array[]): Uint8Array {
  let total = 0;
  for (const p of parts) total += p.length;
  const out = new Uint8Array(total);
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

export class AttachSession {
  readonly sessionId: string;
  readonly channel: number;

  private readonly sink: TerminalSink;
  private readonly send: AttachOptions["send"];
  private readonly sendFrame: AttachOptions["sendFrame"];
  private readonly events: AttachEvents;
  private readonly queryFilter = new TerminalQueryFilter();
  /** A phone is view-only: the worker refuses its lease claims and its input (docs/protocol/v1.md 4). */
  private readonly viewOnly: boolean;

  private state: AttachState = "detached";
  /** Generation of the screen the client believes it holds; 0 means "nothing retained". */
  private generation = 0;
  private offset = 0n;
  private hasRetained = false;
  /** True while an attach request is outstanding on this connection; cleared by detach. */
  private attachedSent = false;
  private ownsLease = false;
  private cols = 0;
  private rows = 0;
  private pending: PendingSnapshot | undefined;
  /** Output that arrived while a snapshot was open; applied, in order, after snapshot.end. */
  private held: Frame[] = [];
  private lastAck = -1n;
  private sawInput = false;

  constructor(opts: AttachOptions) {
    this.sessionId = opts.sessionId;
    this.channel = opts.channel;
    this.sink = opts.sink;
    this.send = opts.send;
    this.sendFrame = opts.sendFrame;
    this.events = opts.events;
    this.viewOnly = opts.kind === "ios";
  }

  get attachState(): AttachState {
    return this.state;
  }

  /** What a later session needs to resume this one's screen: the generation and the offset it has drawn up to. */
  retainedState(): { generation: number; offset: bigint } | undefined {
    return this.hasRetained ? { generation: this.generation, offset: this.offset } : undefined;
  }

  /** Resumes a screen kept from an earlier session: the next attach asks the worker to replay from there. */
  restore(r: { generation: number; offset: bigint }): void {
    this.generation = r.generation;
    this.offset = r.offset;
    this.hasRetained = true;
  }

  get leaseOwner(): boolean {
    return this.ownsLease;
  }

  /** Sends `session.attach`; `retained` is included exactly when a reconnect kept our screen. */
  attach(): void {
    this.setState("attaching", this.hasRetained ? "resuming with retained state" : "attaching");
    this.attachedSent = true;
    this.send({
      type: T.SessionAttach,
      session_id: this.sessionId,
      channel: this.channel,
      retained: this.hasRetained
        ? { generation: this.generation, offset: Number(this.offset) }
        : undefined,
      // Never claim on attach: the CLI attach client owns the size by default. See claimLease().
      claim_lease: false,
      // sink.reset() runs before every snapshot, so the host may send the history with each.
      resets: true,
    });
  }

  /** The socket dropped: forget anything half-applied and keep retained state for the next attach. */
  disconnected(): void {
    this.pending = undefined;
    this.held = [];
    this.ownsLease = false;
    this.attachedSent = false;
    this.sawInput = false;
    if (this.state !== "exited") this.setState("detached", "connection lost");
  }

  /**
   * The daemon epoch changed: the retained screen is meaningless, expect a fresh snapshot. The
   * connection that carried it is gone, so detach first if the worker still sees us attached.
   */
  epochChanged(): void {
    this.resetRetained();
    this.reattach();
  }

  detach(): void {
    this.send({ type: T.SessionDetach, session_id: this.sessionId });
    this.pending = undefined;
    this.held = [];
    this.ownsLease = false;
    this.attachedSent = false;
    this.setState("detached", "detached");
  }

  /**
   * Explicit focus or first input: the browser takes the size lease and resizes the PTY. Callers
   * that know the pane's real size pass it, because the size this session learned on attach is the
   * PTY's, not the pane's, and a claim must not shrink the agent to a stale grid.
   */
  claimLease(cols?: number, rows?: number): void {
    if (this.viewOnly || this.ownsLease) return;
    // Only an attached session can take the lease: a claim sent while the attach is still in
    // flight is refused, and the pane would believe it owned a lease it never got.
    if (this.state !== "live" && this.state !== "replay" && this.state !== "snapshot") return;
    if (cols !== undefined && rows !== undefined && cols > 0 && rows > 0) {
      this.cols = cols;
      this.rows = rows;
    }
    this.ownsLease = true;
    this.send({
      type: T.SessionResize,
      session_id: this.sessionId,
      cols: this.cols,
      rows: this.rows,
      claim_lease: true,
    });
  }

  /** Keystrokes and pastes from xterm.js. Input is best-effort and never replayed. */
  input(data: string | Uint8Array): void {
    if (this.state === "detached" || this.state === "exited" || this.viewOnly) return;
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
    // While another client holds the lease, its real terminal answers the PTY's queries; ours must
    // not answer a second time. Once we hold the lease, xterm.js is the responder and we forward.
    const out = this.ownsLease ? bytes : this.queryFilter.push(bytes);
    // A swallowed query reply is not a keystroke, so it must not be what claims the lease.
    if (out.length === 0) return;
    if (!this.sawInput) {
      this.sawInput = true;
      this.claimLease();
    }
    this.sendFrame(FrameType.Input, 0n, out);
  }

  /** Local pane size changed; only the lease owner may move the PTY. */
  resize(cols: number, rows: number): void {
    if (cols <= 0 || rows <= 0) return;
    const changed = cols !== this.cols || rows !== this.rows;
    this.cols = cols;
    this.rows = rows;
    if (changed && this.ownsLease) this.sendResize();
  }

  /** Names the next byte offset accepted, at most a few times a second (section 5). */
  flushAck(): void {
    if (this.state !== "live" && this.state !== "replay") return;
    if (this.offset === this.lastAck) return;
    if (this.sendFrame(FrameType.Ack, this.offset, new Uint8Array(0))) this.lastAck = this.offset;
  }

  /**
   * Dispatches one control message. Only one terminal is attached at a time in this app, and every
   * message the worker sends now names its session, so a message addressed to another session - or
   * one that cannot belong to the state we are in - is ignored rather than acted on: re-syncing on
   * someone else's snapshot would detach and re-attach us for no reason.
   */
  handleControl(m: ControlMessage): void {
    switch (m.type) {
      case T.SessionAttached:
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        this.onAttached(m);
        break;
      case T.SnapshotBegin:
        // The worker also sends snapshots unprompted - on a generation change or when this client
        // falls out of the ring - so a snapshot may open in every live state, not only while
        // attaching. Output is held until snapshot.end (see onOutput).
        if (this.state === "detached" || this.state === "exited") break;
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        this.pending = {
          id: m.snapshot_id ?? "",
          generation: m.generation ?? 0,
          offset: m.offset ?? 0,
          parts: [],
        };
        this.held = [];
        this.setState("snapshot", "capturing a fresh snapshot");
        break;
      case T.SnapshotEnd:
        if (this.pending === undefined && this.state !== "snapshot") break;
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        this.onSnapshotEnd(m);
        break;
      case T.LeaseChanged:
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        this.onLeaseChanged(m);
        break;
      case T.SessionExit:
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        this.setState("exited", m.exit_code === undefined ? "exited" : `exited (code ${m.exit_code})`);
        this.events.exit(m.exit_code, m.at);
        break;
      case T.Error:
        // Errors for other sessions are not ours to act on.
        if (m.session_id !== undefined && m.session_id !== this.sessionId) break;
        if (m.code === Codes.Forbidden && this.ownsLease) {
          this.ownsLease = false;
          this.events.error(m.code, m.message ?? "another client owns the size");
          break;
        }
        this.events.error(m.code ?? Codes.InvalidArgument, m.message ?? "");
        break;
      default:
        break;
    }
  }

  handleFrame(f: Frame): void {
    if (f.header.channel !== this.channel || f.header.type === FrameType.Control) return;
    switch (f.header.type) {
      case FrameType.Output:
        this.onOutput(f);
        break;
      case FrameType.Snapshot:
        if (this.pending === undefined) {
          // A chunk with no snapshot.begin, or one that arrived after snapshot.end.
          if (this.state !== "detached" && this.state !== "attaching") {
            this.resync("snapshot chunk without snapshot.begin");
          }
          return;
        }
        this.pending.parts.push(f.payload);
        break;
      default:
        break;
    }
  }

  private onAttached(m: ControlMessage): void {
    this.generation = m.generation ?? 0;
    this.cols = m.cols ?? this.cols;
    this.rows = m.rows ?? this.rows;
    this.pending = undefined;
    this.held = [];
    if (m.mode === Mode.Snapshot) {
      this.setState("snapshot", "capturing a fresh snapshot");
      return;
    }
    // Replay: the ring still holds what we are missing, so bytes follow from our own offset.
    this.offset = m.offset !== undefined ? BigInt(m.offset) : this.offset;
    this.hasRetained = true;
    this.setState("replay", `replaying from ${this.offset}`);
  }

  private onOutput(f: Frame): void {
    if (this.pending !== undefined) {
      // The worker serialises snapshot.begin/chunks/end with live output, but should a live frame
      // slip in on this channel while a snapshot is open, it is held rather than drawn over the
      // stale screen: the snapshot is applied only at snapshot.end, and the held bytes follow.
      this.held.push(f);
      return;
    }
    const seq = f.header.seq;
    if (this.hasRetained && this.offset !== seq && this.state === "replay") {
      this.resync(`output gap: expected ${this.offset}, got ${seq}`);
      return;
    }
    this.sink.write(f.payload);
    this.offset = seq + BigInt(f.payload.length);
    this.hasRetained = true;
    if (this.state !== "live") this.setState("live", "live");
  }

  private onSnapshotEnd(m: ControlMessage): void {
    const pending = this.pending;
    this.pending = undefined;
    if (pending === undefined) {
      this.resync("snapshot.end without snapshot.begin");
      return;
    }
    if (m.snapshot_id !== undefined && pending.id !== "" && m.snapshot_id !== pending.id) {
      this.resync("snapshot.end names another snapshot");
      return;
    }
    if (m.parts !== undefined && m.parts !== pending.parts.length) {
      this.resync(`snapshot incomplete: ${pending.parts.length} of ${m.parts} parts`);
      return;
    }
    this.sink.reset();
    this.sink.write(concat(pending.parts));
    this.generation = pending.generation;
    this.offset = BigInt(pending.offset);
    this.hasRetained = true;
    this.setState("live", "snapshot applied");
    this.flushHeld();
  }

  /** Applies output that was held while a snapshot was open, in the order the worker sent it. */
  private flushHeld(): void {
    const held = this.held;
    this.held = [];
    for (const f of held) {
      if (this.state === "detached" || this.state === "exited" || this.pending !== undefined) return;
      if (f.header.seq !== this.offset) {
        this.resync(`held output gap: expected ${this.offset}, got ${f.header.seq}`);
        return;
      }
      this.onOutput(f);
    }
  }

  /** Drops everything we thought we knew and attaches again for a snapshot. */
  private resync(reason: string): void {
    this.events.error(Codes.InvalidArgument, reason);
    this.resetRetained();
    this.reattach();
  }

  private resetRetained(): void {
    this.pending = undefined;
    this.held = [];
    this.generation = 0;
    this.offset = 0n;
    this.lastAck = -1n;
    this.hasRetained = false;
    this.ownsLease = false;
    // The next keystroke may claim the lease again, exactly as it may on a fresh attach.
    this.sawInput = false;
  }

  /**
   * Attaches again for a fresh snapshot. The worker rejects a second `session.attach` on a live
   * attachment with `conflict`, so a detach goes out first whenever one is still outstanding.
   */
  private reattach(): void {
    if (this.attachedSent) {
      this.send({ type: T.SessionDetach, session_id: this.sessionId });
      this.attachedSent = false;
    }
    this.attach();
  }

  /**
   * The worker told every attached client who holds the size lease. Losing it stops this pane from
   * answering terminal queries and from sending resizes; regaining both restores them and realigns
   * the PTY with this pane's size, which the previous owner may have changed.
   */
  private onLeaseChanged(m: ControlMessage): void {
    const owner = m.owner === true;
    if (this.state === "detached" || this.state === "exited") return;
    if (owner === this.ownsLease) return;
    this.ownsLease = owner;
    if (!owner) return;
    if (
      this.cols > 0 &&
      this.rows > 0 &&
      (m.cols !== this.cols || m.rows !== this.rows)
    ) {
      this.sendResize();
    }
  }

  private sendResize(): void {
    this.send({
      type: T.SessionResize,
      session_id: this.sessionId,
      cols: this.cols,
      rows: this.rows,
    });
  }

  private setState(state: AttachState, detail: string): void {
    if (this.state === state && state !== "exited") return;
    this.state = state;
    this.events.state(state, detail);
  }
}
