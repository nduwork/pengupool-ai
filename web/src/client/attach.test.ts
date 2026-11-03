// The attach state machine: replay vs snapshot, the size lease, input and acks. The terminal sink
// is a fake, so these tests run without a DOM and without xterm.js.
import { describe, expect, it } from "vitest";

import { AttachSession, type AttachState } from "./attach";
import { T, type ControlMessage } from "../protocol/control";
import { FrameType, type Frame } from "../protocol/frame";

class Harness {
  readonly session: AttachSession;
  readonly sent: Omit<ControlMessage, "id">[] = [];
  readonly frames: { type: FrameType; seq: bigint; payload: Uint8Array }[] = [];
  readonly writes: Uint8Array[] = [];
  resets = 0;
  states: AttachState[] = [];
  errors: { code: string; message: string }[] = [];
  exits: { code?: number; at?: string }[] = [];

  constructor(kind?: string) {
    const self = this;
    this.session = new AttachSession({
      sessionId: "sess-1",
      channel: 3,
      kind,
      sink: {
        write: (data) => self.writes.push(data),
        reset: () => {
          self.resets += 1;
        },
      },
      send: (m) => {
        self.sent.push(m);
        return true;
      },
      sendFrame: (type, seq, payload) => {
        self.frames.push({ type, seq, payload });
        return true;
      },
      events: {
        state: (state) => self.states.push(state),
        error: (code, message) => self.errors.push({ code, message }),
        exit: (code, at) => self.exits.push({ code, at }),
      },
    });
  }

  control(m: Partial<ControlMessage> & { type: string }): void {
    this.session.handleControl({ id: "req", ...m } as ControlMessage);
  }

  frame(type: FrameType, seq: number, payload: Uint8Array): void {
    const f: Frame = { header: { type, channel: 3, seq: BigInt(seq), length: payload.length }, payload };
    this.session.handleFrame(f);
  }

  output(seq: number, text: string): void {
    this.frame(FrameType.Output, seq, new TextEncoder().encode(text));
  }

  text(i = 0): string {
    return new TextDecoder().decode(this.writes[i] ?? new Uint8Array(0));
  }

  lastSend(): Omit<ControlMessage, "id"> {
    return this.sent[this.sent.length - 1];
  }
}

function attachMessages(h: Harness): Omit<ControlMessage, "id">[] {
  return h.sent.filter((m) => m.type === T.SessionAttach);
}

describe("AttachSession", () => {
  it("never claims the size lease on attach, and sends no retained state the first time", () => {
    const h = new Harness();
    h.session.attach();
    expect(attachMessages(h)).toHaveLength(1);
    expect(h.lastSend()).toMatchObject({
      type: T.SessionAttach,
      session_id: "sess-1",
      channel: 3,
      claim_lease: false,
      retained: undefined,
    });
    expect(h.lastSend().retained).toBeUndefined();
  });

  it("sends retained generation and offset when reconnecting", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 7, cols: 80, rows: 24, offset: 100 });
    h.output(100, "abc");
    h.session.disconnected();
    expect(h.states).toContain("detached");

    h.session.attach();
    expect(h.lastSend().retained).toEqual({ generation: 7, offset: 103 });
  });

  it("applies a snapshot only after snapshot.end, and only then resets the screen", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "snapshot", generation: 2, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotBegin, snapshot_id: "snap-1", generation: 2, cols: 80, rows: 24, offset: 4096 });
    h.frame(FrameType.Snapshot, 0, new TextEncoder().encode("row 1"));
    h.frame(FrameType.Snapshot, 1, new TextEncoder().encode("row 2"));
    expect(h.writes).toHaveLength(0);
    expect(h.session.attachState).toBe("snapshot");

    h.control({ type: T.SnapshotEnd, snapshot_id: "snap-1", parts: 2 });
    expect(h.resets).toBe(1);
    expect(h.text()).toBe("row 1row 2");
    expect(h.session.attachState).toBe("live");

    // Live output continues from the captured offset and is appended in order.
    h.output(4096, "live");
    expect(h.text(1)).toBe("live");
    h.session.flushAck();
    expect(h.frames.at(-1)).toMatchObject({ type: FrameType.Ack, seq: 4100n });
  });

  it("discards an incomplete snapshot whole and attaches again", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "snapshot", generation: 5, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotBegin, snapshot_id: "snap-2", generation: 5, cols: 80, rows: 24, offset: 10 });
    h.frame(FrameType.Snapshot, 0, new TextEncoder().encode("half"));
    h.control({ type: T.SnapshotEnd, snapshot_id: "snap-2", parts: 3 });
    expect(h.writes).toHaveLength(0);
    expect(h.resets).toBe(0);
    expect(h.errors.at(-1)?.message).toContain("snapshot incomplete");
    expect(h.sent.at(-2)).toMatchObject({ type: T.SessionDetach, session_id: "sess-1" });
    expect(h.lastSend()).toMatchObject({ type: T.SessionAttach, claim_lease: false });
    expect(attachMessages(h)).toHaveLength(2);
  });

  it("applies an unprompted snapshot sent while live, holding live bytes until snapshot.end", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 3, cols: 80, rows: 24, offset: 0 });
    h.output(0, "old screen");
    expect(h.session.attachState).toBe("live");

    // The worker sends a snapshot on its own when the generation changes or this client falls out
    // of the ring. It is accepted in the live state, not only while attaching.
    h.control({ type: T.SnapshotBegin, snapshot_id: "gen-4", generation: 4, cols: 80, rows: 24, offset: 11 });
    expect(h.session.attachState).toBe("snapshot");

    // Live bytes that arrive while the snapshot is open are held, not drawn over the stale screen.
    h.output(11, "live during snapshot");
    expect(h.writes).toHaveLength(1);

    h.frame(FrameType.Snapshot, 0, new TextEncoder().encode("fresh"));
    // The snapshot is applied only at snapshot.end: exactly one reset, then the snapshot, then the
    // held bytes in order.
    expect(h.resets).toBe(0);
    h.control({ type: T.SnapshotEnd, snapshot_id: "gen-4", parts: 1 });
    expect(h.resets).toBe(1);
    expect(h.text(1)).toBe("fresh");
    expect(h.text(2)).toBe("live during snapshot");
    expect(h.session.attachState).toBe("live");
    expect(attachMessages(h)).toHaveLength(1);
  });

  it("resyncs when a held byte does not continue the snapshot offset", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.output(0, "old");
    h.control({ type: T.SnapshotBegin, snapshot_id: "s", generation: 2, cols: 80, rows: 24, offset: 50 });
    h.output(10, "already inside the snapshot");
    h.control({ type: T.SnapshotEnd, snapshot_id: "s", parts: 0 });
    expect(h.errors.at(-1)?.message).toContain("held output gap");
    expect(h.sent.at(-2)).toMatchObject({ type: T.SessionDetach });
    expect(h.sent.at(-1)).toMatchObject({ type: T.SessionAttach });
  });

  it("resyncs on a replay gap instead of writing out of order", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 4, cols: 80, rows: 24, offset: 50 });
    h.output(60, "wrong offset");
    expect(h.writes).toHaveLength(0);
    expect(h.errors.at(-1)?.message).toContain("output gap");
    // The worker answers a second attach on a live attachment with `conflict`: detach first.
    expect(h.sent.at(-2)).toMatchObject({ type: T.SessionDetach });
    expect(h.lastSend()).toMatchObject({ type: T.SessionAttach });
    expect(attachMessages(h)).toHaveLength(2);
  });

  it("drops xterm.js's query replies while another client holds the lease", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "snapshot", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotBegin, snapshot_id: "s", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotEnd, snapshot_id: "s", parts: 0 });

    h.session.input("\u001b[?1;2c"); // DA1 reply
    h.session.input("\u001b[12;40R"); // cursor position report
    expect(h.frames.filter((f) => f.type === FrameType.Input)).toHaveLength(0);

    // The first real keystroke claims the lease, and from then on xterm.js is the responder.
    h.session.input("a");
    expect(h.lastSend()).toMatchObject({ type: T.SessionResize, claim_lease: true });
    expect(h.session.leaseOwner).toBe(true);
    const inputs = h.frames.filter((f) => f.type === FrameType.Input);
    expect(new TextDecoder().decode(inputs[0].payload)).toBe("a");
    expect(inputs[0].seq).toBe(0n);

    h.session.input("\u001b[?1;2c");
    const owned = h.frames.filter((f) => f.type === FrameType.Input);
    expect(new TextDecoder().decode(owned[1].payload)).toBe("\u001b[?1;2c");
  });

  it("claims the lease with the pane's own size, not the size the PTY reported on attach", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "snapshot", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.session.claimLease(141, 31);
    expect(h.lastSend()).toMatchObject({ type: T.SessionResize, claim_lease: true, cols: 141, rows: 31 });
    // A later local fit reports the same size and must not send a second resize.
    h.session.resize(141, 31);
    expect(h.sent.filter((m) => m.type === T.SessionResize)).toHaveLength(1);
  });

  it("forgets retained state and detaches before re-attaching when the daemon epoch changes", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 9, cols: 80, rows: 24, offset: 12 });
    h.output(12, "xy");
    h.session.epochChanged();
    expect(h.sent.at(-2)).toMatchObject({ type: T.SessionDetach, session_id: "sess-1" });
    expect(h.lastSend()).toMatchObject({ type: T.SessionAttach });
    expect(h.lastSend().retained).toBeUndefined();
  });

  it("tracks the lease: losing it stops query answers and resizes, regaining restores both", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.session.claimLease();
    expect(h.session.leaseOwner).toBe(true);
    expect(h.sent.filter((m) => m.type === T.SessionResize)).toHaveLength(1); // the claim

    h.control({
      type: T.LeaseChanged,
      session_id: "sess-1",
      connection_id: "conn-cli",
      owner: false,
      cols: 100,
      rows: 30,
      generation: 1,
    });
    expect(h.session.leaseOwner).toBe(false);

    // No resize leaves the pane while another client owns the PTY.
    h.session.resize(120, 40);
    expect(h.sent.filter((m) => m.type === T.SessionResize)).toHaveLength(1);

    // xterm.js's own query replies are dropped again: we are not the responder. Neither they nor a
    // focus report on blur take the lease back.
    h.session.input("\u001b[?1;2c", 120, 40);
    h.session.input("\u001b[O", 120, 40);
    expect(h.frames.filter((f) => f.type === FrameType.Input)).toHaveLength(0);
    expect(h.sent.filter((m) => m.type === T.SessionResize)).toHaveLength(1);

    // Regaining the lease puts us back in charge, and the PTY is realigned with our pane size.
    h.control({
      type: T.LeaseChanged,
      session_id: "sess-1",
      connection_id: "conn-browser",
      owner: true,
      cols: 100,
      rows: 30,
      generation: 1,
    });
    expect(h.session.leaseOwner).toBe(true);
    expect(h.lastSend()).toMatchObject({ type: T.SessionResize, cols: 120, rows: 40 });
    h.session.input("\u001b[?1;2c");
    const inputs = h.frames.filter((f) => f.type === FrameType.Input);
    expect(inputs).toHaveLength(1);
    expect(new TextDecoder().decode(inputs[0].payload)).toBe("\u001b[?1;2c");
  });

  it("never claims the lease or sends input for a view-only ios client", () => {
    const h = new Harness("ios");
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.session.claimLease();
    expect(h.session.leaseOwner).toBe(false);
    expect(h.sent.filter((m) => m.type === T.SessionResize)).toHaveLength(0);

    h.session.input("a");
    expect(h.frames.filter((f) => f.type === FrameType.Input)).toHaveLength(0);

    // A lease.changed naming another holder changes nothing for a view-only client.
    h.control({ type: T.LeaseChanged, session_id: "sess-1", connection_id: "conn-cli", owner: false });
    expect(h.session.leaseOwner).toBe(false);
  });

  it("ignores frames and errors addressed to another channel or session", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.session.handleFrame({
      header: { type: FrameType.Output, channel: 9, seq: 0n, length: 3 },
      payload: new TextEncoder().encode("no"),
    });
    h.control({ type: T.Error, code: "not_found", message: "other", session_id: "sess-2" });
    expect(h.writes).toHaveLength(0);
    expect(h.errors).toHaveLength(0);
  });

  it("reports a lost lease instead of pretending to own the size", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "snapshot", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotBegin, snapshot_id: "s", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SnapshotEnd, snapshot_id: "s", parts: 0 });
    h.session.claimLease();
    h.control({ type: T.Error, code: "forbidden", message: "another client owns the size" });
    expect(h.session.leaseOwner).toBe(false);
    expect(h.errors.at(-1)?.code).toBe("forbidden");
  });

  it("reports a session exit", () => {
    const h = new Harness();
    h.session.attach();
    h.control({ type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0 });
    h.control({ type: T.SessionExit, exit_code: 3, at: "2026-01-02T03:04:05Z" });
    expect(h.session.attachState).toBe("exited");
    expect(h.exits[0]).toEqual({ code: 3, at: "2026-01-02T03:04:05Z" });
  });
});
