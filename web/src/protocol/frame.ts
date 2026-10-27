// The 17-byte frame header and frame codec of docs/protocol/v1.md section 1, reimplemented in
// TypeScript. There is no shared codegen: the golden bytes in internal/protocol/testdata/v1 are the
// cross-language contract, and frame.test.ts decodes them directly.
import { Codes, errorf } from "./errors";

/** Fixed header size: type:u8 | channel:u32 | seq:u64 | length:u32, all big-endian. */
export const HEADER_SIZE = 17;

// Payload limits, checked before allocation exactly as internal/protocol does.
export const MAX_CONTROL_PAYLOAD = 1 << 20; // 1 MiB
export const MAX_DATA_PAYLOAD = 64 << 10;
export const MAX_INPUT_PAYLOAD = 64 << 10;
export const MAX_SNAPSHOT_PAYLOAD = 64 << 10;

export enum FrameType {
  Control = 0x01,
  Output = 0x02,
  Input = 0x03,
  Ack = 0x04,
  Snapshot = 0x05,
}

/**
 * The largest payload this frame type may carry, or undefined for an unknown type.
 * Unknown types are refused rather than guessed at, so a corrupt stream cannot make us allocate.
 */
export function maxPayload(type: number): number | undefined {
  switch (type) {
    case FrameType.Control:
      return MAX_CONTROL_PAYLOAD;
    case FrameType.Output:
      return MAX_DATA_PAYLOAD;
    case FrameType.Input:
      return MAX_INPUT_PAYLOAD;
    case FrameType.Ack:
      return 0;
    case FrameType.Snapshot:
      return MAX_SNAPSHOT_PAYLOAD;
    default:
      return undefined;
  }
}

export function frameTypeName(type: number): string {
  switch (type) {
    case FrameType.Control:
      return "control";
    case FrameType.Output:
      return "output";
    case FrameType.Input:
      return "input";
    case FrameType.Ack:
      return "ack";
    case FrameType.Snapshot:
      return "snapshot";
    default:
      return `frame(${hexByte(type)})`;
  }
}

function hexByte(b: number): string {
  return `0x${(b & 0xff).toString(16).padStart(2, "0")}`;
}

/** A decoded 17-byte frame header. `seq` is a uint64, so it is a bigint. */
export interface Header {
  type: FrameType;
  channel: number;
  seq: bigint;
  length: number;
}

export interface Frame {
  header: Header;
  payload: Uint8Array;
}

/** Encodes a header into its 17-byte big-endian wire form. */
export function encodeHeader(h: Header): Uint8Array {
  const b = new Uint8Array(HEADER_SIZE);
  const dv = new DataView(b.buffer);
  dv.setUint8(0, h.type);
  dv.setUint32(1, h.channel);
  dv.setBigUint64(5, h.seq);
  dv.setUint32(13, h.length);
  return b;
}

/**
 * Decodes and validates a header. Length limits are checked before any payload is read, so a
 * hostile `length` can never make the caller allocate.
 */
export function parseHeader(b: Uint8Array): Header {
  if (b.length < HEADER_SIZE) {
    throw errorf(Codes.TruncatedFrame, `header needs ${HEADER_SIZE} bytes, got ${b.length}`);
  }
  const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const h: Header = {
    type: dv.getUint8(0) as FrameType,
    channel: dv.getUint32(1),
    seq: dv.getBigUint64(5),
    length: dv.getUint32(13),
  };
  const max = maxPayload(h.type);
  if (max === undefined) {
    throw errorf(Codes.UnknownFrameType, `unknown frame type ${hexByte(h.type)}`);
  }
  if (h.length > max) {
    throw errorf(
      Codes.FrameTooLarge,
      `${frameTypeName(h.type)} frame length ${h.length} exceeds limit ${max}`,
    );
  }
  if (h.type === FrameType.Control && h.channel !== 0) {
    throw errorf(Codes.InvalidChannel, `control frame must use channel 0, got ${h.channel}`);
  }
  if (h.type !== FrameType.Control && h.channel === 0) {
    throw errorf(Codes.InvalidChannel, `${frameTypeName(h.type)} frame must use a nonzero channel`);
  }
  return h;
}

/** True when `b` is valid UTF-8, the rule for every control payload. */
export function isValidUTF8(b: Uint8Array): boolean {
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(b);
    return true;
  } catch {
    return false;
  }
}

/** Decodes a control payload to text, refusing invalid UTF-8 with `protocol.invalid_utf8`. */
export function decodeControlPayload(b: Uint8Array): string {
  if (!isValidUTF8(b)) {
    throw errorf(Codes.InvalidUTF8, "control payload is not valid UTF-8");
  }
  return new TextDecoder("utf-8").decode(b);
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

/** Encodes a complete frame, enforcing the per-type payload limit. */
export function encodeFrame(f: Frame): Uint8Array {
  const max = maxPayload(f.header.type);
  if (max === undefined) {
    throw errorf(Codes.UnknownFrameType, `unknown frame type ${hexByte(f.header.type)}`);
  }
  if (f.payload.length > max) {
    throw errorf(
      Codes.FrameTooLarge,
      `${frameTypeName(f.header.type)} frame length ${f.payload.length} exceeds limit ${max}`,
    );
  }
  if (f.header.type === FrameType.Control && f.header.channel !== 0) {
    throw errorf(Codes.InvalidChannel, `control frame must use channel 0, got ${f.header.channel}`);
  }
  if (f.header.type !== FrameType.Control && f.header.channel === 0) {
    throw errorf(
      Codes.InvalidChannel,
      `${frameTypeName(f.header.type)} frame must use a nonzero channel`,
    );
  }
  const header = encodeHeader({ ...f.header, length: f.payload.length });
  return concat(header, f.payload);
}

/** Decodes exactly one complete frame from a buffer that holds at least one whole frame. */
export function decodeFrame(bytes: Uint8Array): Frame {
  const header = parseHeader(bytes);
  const total = HEADER_SIZE + header.length;
  if (bytes.length < total) {
    throw errorf(
      Codes.TruncatedFrame,
      `frame needs ${total} bytes, got ${bytes.length}`,
    );
  }
  const payload = bytes.slice(HEADER_SIZE, total);
  if (header.type === FrameType.Control && !isValidUTF8(payload)) {
    throw errorf(Codes.InvalidUTF8, "control payload is not valid UTF-8");
  }
  return { header, payload };
}

/**
 * Incremental decoder for a byte stream (one WebSocket message, or several concatenated).
 * A frame that cannot be parsed leaves the stream position unknown, so the error propagates and
 * the caller drops the connection: a protocol violation is never recovered from in place.
 */
export class FrameDecoder {
  private buf: Uint8Array = new Uint8Array(0);

  /** Feeds bytes and returns every frame that is now complete. */
  push(chunk: Uint8Array): Frame[] {
    this.buf = concat(this.buf, chunk);
    const frames: Frame[] = [];
    for (;;) {
      if (this.buf.length < HEADER_SIZE) break;
      const header = parseHeader(this.buf);
      const total = HEADER_SIZE + header.length;
      if (this.buf.length < total) break;
      const payload = this.buf.slice(HEADER_SIZE, total);
      if (header.type === FrameType.Control && !isValidUTF8(payload)) {
        throw errorf(Codes.InvalidUTF8, "control payload is not valid UTF-8");
      }
      frames.push({ header, payload });
      this.buf = this.buf.slice(total);
    }
    return frames;
  }

  /** Bytes held back because no complete frame was available yet. */
  get pending(): number {
    return this.buf.length;
  }
}
