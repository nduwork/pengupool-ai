// Terminal-query responder filter (docs/protocol/v1.md section 4).
//
// xterm.js answers terminal queries itself: ask it for device attributes (DA1/DA2), a DECRQM mode
// report, a cursor position, a focus event or an in-band resize, and it queues a reply through
// `onData`. While the CLI attach client holds the size lease, that real terminal already answered
// the query, so forwarding xterm.js's copy would inject `\x1b[?1;2c`-style bytes into agent input a
// second time and can leave the harness in the wrong keyboard mode. These replies are therefore
// dropped from what the browser forwards as input.
//
// Only replies are dropped (terminal -> host), never requests (host -> terminal): the app's own
// `\x1b[c` arrives as PTY output, is written to the screen by xterm.js, and never passes through here.

const ESC = 0x1b;
const BEL = 0x07;

/** A hold shorter than this cannot be a complete sequence; longer, and we stop stalling input. */
const MAX_PENDING = 4096;

/**
 * Every CSI query xterm.js answers on its own, as `registerCsiHandler` identifiers. Registering a
 * handler that returns true makes the parser swallow the query before the reply is generated,
 * which is the only way to keep the reply out of `onData` in the first place. An undefined
 * `prefix` or `intermediates` matches any, so one entry can cover several private variants.
 */
export const TERMINAL_QUERY_CSI: { prefix?: string; intermediates?: string; final: string }[] = [
  { final: "c" }, // DA1
  { prefix: ">", final: "c" }, // DA2
  { prefix: "?", final: "c" }, // DA3
  { final: "n" }, // DSR: cursor position / device status -> CSI r;c R
  { prefix: "?", intermediates: "$", final: "p" }, // DECRQM
  { intermediates: "$", final: "p" }, // DECRQM, ANSI form
  { final: "R" }, // a cursor position report that slipped through
  { final: "t" }, // window report and in-band resize (mode 2048)
  { prefix: "?", final: "u" }, // kitty keyboard flags
  { prefix: ">", final: "u" }, // kitty keyboard push/pop
  { prefix: ">", final: "q" }, // XTVERSION
  { final: "q" }, // DECLL
  { final: "I" }, // focus in (mode 1004)
  { final: "O" }, // focus out
];

/** OSC queries xterm.js answers with an OSC reply: foreground/background/cursor colour, palette. */
export const TERMINAL_QUERY_OSC: number[] = [4, 10, 11, 12];

// ESC ] OSC, ESC P DCS, ESC X SOS, ESC ^ PM, ESC _ APC: string-like escapes, passed through whole.
const STRING_INTRO: Record<number, true> = { 0x5d: true, 0x50: true, 0x58: true, 0x5e: true, 0x5f: true };

function findStringEnd(buf: Uint8Array, from: number): number {
  for (let j = from; j < buf.length; j++) {
    if (buf[j] === BEL) return j + 1;
    if (buf[j] === ESC && buf[j + 1] === 0x5c) return j + 2;
  }
  return -1;
}

/** True when a complete CSI sequence (including ESC [) is a terminal reply we must not forward. */
function isTerminalReply(seq: Uint8Array): boolean {
  const final = seq[seq.length - 1];
  const body = seq.subarray(2, seq.length - 1);
  switch (final) {
    // DA1 / DA2 / DA3 (primary and secondary device attributes).
    case 0x63: // 'c'
    // DECRQM and DECRQM/ANSI mode reports.
    case 0x70: // 'p'
    // Cursor position report (DSR reply).
    case 0x52: // 'R'
      return true;
    // Focus in / focus out (mode 1004), which carry no parameters.
    case 0x49: // 'I'
    case 0x4f: // 'O'
      return body.length === 0;
    // Window / in-band resize reports (xterm mode 2048): CSI 48 ; rows ; cols t, CSI 4 ; … t.
    case 0x74: {
      // 't'
      const first = firstNumber(body);
      return first === 4 || first === 48;
    }
    default:
      return false;
  }
}

function firstNumber(body: Uint8Array): number | undefined {
  let i = 0;
  while (i < body.length && (body[i] === 0x3f || body[i] === 0x3e || body[i] === 0x3d)) i++;
  let n = 0;
  let seen = false;
  while (i < body.length && body[i]! >= 0x30 && body[i]! <= 0x39) {
    n = n * 10 + (body[i]! - 0x30);
    seen = true;
    i++;
  }
  return seen ? n : undefined;
}

/**
 * Removes terminal replies from a byte stream. Stateful across calls: a sequence split over two
 * `onData` events is held rather than half-forwarded.
 */
export class TerminalQueryFilter {
  private held = new Uint8Array(0);

  push(chunk: Uint8Array): Uint8Array {
    let buf = chunk;
    if (this.held.length > 0) {
      const joined = new Uint8Array(this.held.length + chunk.length);
      joined.set(this.held, 0);
      joined.set(chunk, this.held.length);
      buf = joined;
      this.held = new Uint8Array(0);
    }

    const out: number[] = [];
    let i = 0;
    while (i < buf.length) {
      if (buf[i] !== ESC) {
        out.push(buf[i]);
        i++;
        continue;
      }
      if (i + 1 >= buf.length) break; // hold the lone ESC
      const next = buf[i + 1];
      if (next === 0x5b) {
        let j = i + 2;
        while (j < buf.length && !(buf[j]! >= 0x40 && buf[j]! <= 0x7e)) j++;
        if (j >= buf.length) break; // incomplete CSI: hold it
        const seq = buf.subarray(i, j + 1);
        if (!isTerminalReply(seq)) {
          for (let k = i; k <= j; k++) out.push(buf[k]!);
        }
        i = j + 1;
        continue;
      }
      if (STRING_INTRO[next] === true) {
        const end = findStringEnd(buf, i + 2);
        if (end < 0) break; // incomplete string escape: hold it
        for (let k = i; k < end; k++) out.push(buf[k]!);
        i = end;
        continue;
      }
      out.push(buf[i], next); // two-byte escape, not a query
      i += 2;
    }

    const tail = buf.subarray(i);
    this.held = tail.length > MAX_PENDING ? new Uint8Array(0) : new Uint8Array(tail);
    if (tail.length > MAX_PENDING) {
      for (const b of tail) out.push(b);
    }
    return Uint8Array.from(out);
  }

  /** Bytes held back because a sequence was still incomplete; zero in steady state. */
  get pending(): number {
    return this.held.length;
  }

  /** Emits anything still held back, e.g. on detach. */
  flush(): Uint8Array {
    const held = this.held;
    this.held = new Uint8Array(0);
    return held;
  }
}
