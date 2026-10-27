// Golden-frame tests against the cross-language fixtures in internal/protocol/testdata/v1. Those bytes are
// generated and checked by internal/protocol TestGenerateFixtures/TestGoldenFixtures, so this file
// is the TypeScript half of the same contract: a framing change must break both languages at once.
import { describe, expect, it } from "vitest";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Codes, ProtocolError } from "./errors";
import {
  FrameDecoder,
  FrameType,
  HEADER_SIZE,
  MAX_CONTROL_PAYLOAD,
  MAX_DATA_PAYLOAD,
  decodeFrame,
  encodeFrame,
  encodeHeader,
  frameTypeName,
  maxPayload,
  parseHeader,
  type Frame,
} from "./frame";
import {
  T,
  decodeControl,
  encodeControl,
  isError,
  type ControlMessage,
} from "./control";
import { newID } from "./uuid";

const fixturesDir = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "..",
  "internal",
  "protocol",
  "testdata",
  "v1",
);

interface FixtureMeta {
  name: string;
  type: number;
  channel: number;
  seq: number;
  length: number;
  payload_utf8?: string;
  payload_hex?: string;
  expect_error?: string;
  truncate_to?: number;
}

function loadFixtures(): { meta: FixtureMeta; wire: Uint8Array }[] {
  if (!existsSync(fixturesDir)) {
    throw new Error(
      `golden frames missing at ${fixturesDir}; run "go test ./internal/protocol/ -run TestGenerateFixtures"`,
    );
  }
  return readdirSync(fixturesDir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((json) => {
      const meta = JSON.parse(readFileSync(join(fixturesDir, json), "utf8")) as FixtureMeta;
      const wire = new Uint8Array(readFileSync(join(fixturesDir, `${meta.name}.bin`)));
      return { meta, wire };
    });
}

function toHex(b: Uint8Array): string {
  return Buffer.from(b).toString("hex");
}

function expectCode(fn: () => unknown, code: string): void {
  try {
    fn();
  } catch (err) {
    expect(err).toBeInstanceOf(ProtocolError);
    expect((err as ProtocolError).code).toBe(code);
    return;
  }
  throw new Error(`expected ${code}, nothing was thrown`);
}

const fixtures = loadFixtures();

describe("golden frames (internal/protocol/testdata/v1)", () => {
  it("covers the documented set", () => {
    expect(fixtures.map((f) => f.meta.name)).toEqual([
      "ack_seq8192",
      "control_hello",
      "output_seq4096",
      "oversize_length",
      "snapshot_part3",
      "truncated_header",
    ]);
  });

  for (const { meta, wire } of fixtures) {
    if (meta.expect_error) {
      it(`${meta.name}: rejects with ${meta.expect_error}`, () => {
        const err = (() => {
          try {
            decodeFrame(wire);
          } catch (e) {
            return e as ProtocolError;
          }
          return undefined;
        })();
        expect(err?.code).toBe(meta.expect_error);
      });
      continue;
    }

    it(`${meta.name}: decodes and re-encodes byte for byte`, () => {
      const frame = decodeFrame(wire);
      expect({
        type: frame.header.type,
        channel: frame.header.channel,
        seq: Number(frame.header.seq),
        length: frame.header.length,
      }).toEqual({
        type: meta.type,
        channel: meta.channel,
        seq: meta.seq,
        length: meta.length,
      });
      if (meta.payload_utf8 !== undefined) {
        expect(new TextDecoder().decode(frame.payload)).toBe(meta.payload_utf8);
      }
      if (meta.payload_hex !== undefined) {
        expect(toHex(frame.payload)).toBe(meta.payload_hex);
      }
      expect(wire).toHaveLength(HEADER_SIZE + meta.length);
      expect(toHex(encodeFrame(frame))).toBe(toHex(wire));
    });
  }

  it("control_hello decodes to the expected message", () => {
    const { wire } = fixtures.find((f) => f.meta.name === "control_hello")!;
    const m = decodeControl(decodeFrame(wire));
    expect(m.id).toBe("0199a5f1-0000-7000-8000-000000000001");
    expect(m.type).toBe(T.Hello);
    expect(m.v).toBe(1);
    expect(m.client).toEqual({ kind: "cli", name: "pengupool attach", version: "0.1.0-prototype" });
  });
});

describe("header codec", () => {
  it("round-trips a full-width header", () => {
    const h = { type: FrameType.Snapshot, channel: 0xdeadbeef, seq: 1n << 40n, length: 12345 };
    expect(parseHeader(encodeHeader(h))).toEqual(h);
  });

  it("is big-endian at every field", () => {
    const wire = encodeHeader({ type: FrameType.Output, channel: 1, seq: 4096n, length: 22 });
    expect(toHex(wire.slice(0, 17))).toBe("02000000010000000000001000000000" + "16");
  });

  it("rejects a short header before it looks at any field", () => {
    expectCode(() => parseHeader(new Uint8Array(HEADER_SIZE - 1)), Codes.TruncatedFrame);
    expectCode(() => parseHeader(new Uint8Array(0)), Codes.TruncatedFrame);
  });

  it("rejects an unknown frame type", () => {
    const wire = encodeHeader({ type: FrameType.Output, channel: 1, seq: 0n, length: 0 });
    wire[0] = 0x7f;
    expectCode(() => parseHeader(wire), Codes.UnknownFrameType);
  });

  it("rejects an over-limit length before allocating", () => {
    const wire = encodeHeader({
      type: FrameType.Output,
      channel: 1,
      seq: 0n,
      length: MAX_DATA_PAYLOAD + 1,
    });
    expectCode(() => parseHeader(wire), Codes.FrameTooLarge);
  });

  it("rejects a control frame on a nonzero channel and data on channel 0", () => {
    const control = encodeHeader({ type: FrameType.Control, channel: 1, seq: 0n, length: 0 });
    expectCode(() => parseHeader(control), Codes.InvalidChannel);
    const output = encodeHeader({ type: FrameType.Output, channel: 0, seq: 0n, length: 0 });
    expectCode(() => parseHeader(output), Codes.InvalidChannel);
  });

  it("reports the documented limits and names", () => {
    expect(maxPayload(FrameType.Control)).toBe(MAX_CONTROL_PAYLOAD);
    expect(maxPayload(FrameType.Output)).toBe(MAX_DATA_PAYLOAD);
    expect(maxPayload(FrameType.Input)).toBe(MAX_DATA_PAYLOAD);
    expect(maxPayload(FrameType.Ack)).toBe(0);
    expect(maxPayload(FrameType.Snapshot)).toBe(MAX_DATA_PAYLOAD);
    expect(maxPayload(0x00)).toBeUndefined();
    expect(frameTypeName(0x02)).toBe("output");
    expect(frameTypeName(0x7f)).toBe("frame(0x7f)");
  });
});

describe("frame codec", () => {
  it("rejects a truncated payload", () => {
    const header = encodeHeader({ type: FrameType.Output, channel: 1, seq: 0n, length: 64 });
    expectCode(() => decodeFrame(header.subarray(0, 30)), Codes.TruncatedFrame);
  });

  it("rejects a control payload that is not valid UTF-8", () => {
    const payload = Uint8Array.from([0x7b, 0x22, 0xff, 0xfe, 0x22, 0x7d]);
    const wire = encodeFrame({
      header: { type: FrameType.Control, channel: 0, seq: 0n, length: payload.length },
      payload,
    });
    expectCode(() => decodeFrame(wire), Codes.InvalidUTF8);
  });

  it("refuses to encode an over-limit or mischanneled frame", () => {
    const big = new Uint8Array(MAX_DATA_PAYLOAD + 1);
    expectCode(
      () => encodeFrame({ header: { type: FrameType.Input, channel: 1, seq: 0n, length: 0 }, payload: big }),
      Codes.FrameTooLarge,
    );
    expectCode(
      () => encodeFrame({ header: { type: FrameType.Input, channel: 0, seq: 0n, length: 0 }, payload: new Uint8Array(0) }),
      Codes.InvalidChannel,
    );
  });

  it("refuses an ack with a payload", () => {
    expectCode(
      () => encodeFrame({ header: { type: FrameType.Ack, channel: 1, seq: 1n, length: 1 }, payload: Uint8Array.from([1]) }),
      Codes.FrameTooLarge,
    );
  });

  it("encodes an ack as a bare 17-byte header", () => {
    const wire = encodeFrame({ header: { type: FrameType.Ack, channel: 1, seq: 8192n, length: 0 }, payload: new Uint8Array(0) });
    expect(wire).toHaveLength(HEADER_SIZE);
    expect(toHex(wire)).toBe("0400000001000000000000200000000000");
  });
});

describe("FrameDecoder", () => {
  it("splits a concatenated stream into frames", () => {
    const a: Frame = {
      header: { type: FrameType.Input, channel: 3, seq: 0n, length: 5 },
      payload: new TextEncoder().encode("hello"),
    };
    const b: Frame = {
      header: { type: FrameType.Ack, channel: 3, seq: 5n, length: 0 },
      payload: new Uint8Array(0),
    };
    const wire = new Uint8Array([...encodeFrame(a), ...encodeFrame(b)]);
    const frames = new FrameDecoder().push(wire);
    expect(frames).toHaveLength(2);
    expect(frames[0]!.header.channel).toBe(3);
    expect(new TextDecoder().decode(frames[0]!.payload)).toBe("hello");
    expect(frames[1]!.header.type).toBe(FrameType.Ack);
  });

  it("holds a frame split across chunks and releases it whole", () => {
    const { wire } = fixtures.find((f) => f.meta.name === "output_seq4096")!;
    const decoder = new FrameDecoder();
    expect(decoder.push(wire.slice(0, 9))).toHaveLength(0);
    expect(decoder.push(wire.slice(9, 20))).toHaveLength(0);
    const frames = decoder.push(wire.slice(20));
    expect(frames).toHaveLength(1);
    expect(frames[0]!.header.seq).toBe(4096n);
    expect(decoder.pending).toBe(0);
  });

  it("surfaces a malformed header instead of resynchronising", () => {
    const decoder = new FrameDecoder();
    const wire = encodeHeader({ type: FrameType.Output, channel: 1, seq: 0n, length: MAX_DATA_PAYLOAD + 1 });
    expectCode(() => decoder.push(wire), Codes.FrameTooLarge);
  });
});

describe("control messages", () => {
  it("round-trips a hello through encodeControl/decodeControl", () => {
    const hello: ControlMessage = {
      id: newID(1_700_000_000_000),
      type: T.Hello,
      v: 1,
      client: { kind: "browser", name: "pengupool web", version: "0.1.0-prototype" },
      claim_lease: false,
    };
    const frame = decodeFrame(encodeControl(hello));
    expect(decodeControl(frame)).toEqual(hello);
    expect(frame.header.type).toBe(FrameType.Control);
    expect(frame.header.channel).toBe(0);
  });

  it("carries the size-lease claim on session.resize", () => {
    const msg: ControlMessage = {
      id: newID(),
      type: T.SessionResize,
      session_id: "s1",
      cols: 120,
      rows: 40,
      claim_lease: true,
    };
    const back = decodeControl(decodeFrame(encodeControl(msg)));
    expect(back.claim_lease).toBe(true);
    expect(back.cols).toBe(120);
    expect(back.rows).toBe(40);
  });

  it("rejects a payload without an id or a type, and a non-object", () => {
    for (const payload of ['{"type":"ping"}', '{"id":"x"}', "[]", "null", "not json"]) {
      const wire = encodeFrame({
        header: { type: FrameType.Control, channel: 0, seq: 0n, length: 0 },
        payload: new TextEncoder().encode(payload),
      });
      expectCode(() => decodeControl(decodeFrame(wire)), Codes.InvalidArgument);
    }
  });

  it("refuses a control message above the 1 MiB limit", () => {
    const big: ControlMessage = {
      id: newID(),
      type: T.Error,
      message: "x".repeat(MAX_CONTROL_PAYLOAD),
    };
    expectCode(() => encodeControl(big), Codes.FrameTooLarge);
  });

  it("marks error responses", () => {
    expect(
      isError({ id: "1", type: T.Error, code: Codes.NotFound, message: "session not attached" }),
    ).toBe(true);
    expect(isError({ id: "1", type: T.Pong })).toBe(false);
  });
});

describe("uuidv7", () => {
  it("lays out the timestamp, version and variant fields", () => {
    // 1_700_000_000_123 ms is 0x018bcfe5687b, so the id starts 018bcfe5-687b-7…
    const id = newID(1_700_000_000_123);
    expect(id).toMatch(/^018bcfe5-687b-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("is monotonic by timestamp and unique within one millisecond", () => {
    const early = newID(1_000);
    const late = newID(2_000);
    expect(early.slice(0, 13) < late.slice(0, 13)).toBe(true);
    const ids = new Set(Array.from({ length: 64 }, () => newID(5_000)));
    expect(ids.size).toBe(64);
  });
});
