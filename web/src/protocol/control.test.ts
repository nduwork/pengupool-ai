// The control schema must match internal/protocol/control.go: the daemon answers a capability
// probe from `hello.ok` with this exact list, and a message type this client does not know is a
// message it silently mishandles (docs/design/review-002-findings.md D3).
import { describe, expect, it } from "vitest";

import { CAPABILITIES, T, decodeControl, encodeControl } from "./control";
import { FrameType, HEADER_SIZE, type Frame } from "./frame";

describe("control message types", () => {
  it("names every v1 message the Go side defines", () => {
    expect(T.LeaseChanged).toBe("lease.changed");
    expect(T.SessionCreate).toBe("session.create");
    expect(T.SessionList).toBe("session.list");
    expect(T.SessionKill).toBe("session.kill");
    expect(T.SnapshotBegin).toBe("snapshot.begin");
    expect(T.SnapshotEnd).toBe("snapshot.end");
  });

  it("advertises the same nine capabilities, in the Go order", () => {
    expect([...CAPABILITIES]).toEqual([
      "state.subscribe",
      "session.attach",
      "session.input",
      "session.resize",
      "session.snapshot",
      "session.create",
      "session.list",
      "session.kill",
      "session.lease",
    ]);
  });

  it("round-trips a lease.changed with its owner flag", () => {
    const bytes = encodeControl({
      id: "req-1",
      type: T.LeaseChanged,
      session_id: "sess-1",
      connection_id: "conn-2",
      owner: true,
      cols: 120,
      rows: 40,
      generation: 7,
    });
    const frame: Frame = {
      header: {
        type: FrameType.Control,
        channel: 0,
        seq: 0n,
        length: bytes.length - HEADER_SIZE,
      },
      payload: bytes.subarray(HEADER_SIZE),
    };
    const back = decodeControl(frame);
    expect(back).toMatchObject({
      type: T.LeaseChanged,
      session_id: "sess-1",
      connection_id: "conn-2",
      owner: true,
      cols: 120,
      rows: 40,
      generation: 7,
    });
  });
});
