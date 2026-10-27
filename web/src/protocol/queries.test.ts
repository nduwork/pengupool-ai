// The terminal-query responder filter: xterm.js answers DA1/DA2, DECRQM, cursor position, focus and
// in-band resize queries itself, and those replies must not reach the PTY while the CLI attach
// client holds the lease (docs/protocol/v1.md section 4).
import { describe, expect, it } from "vitest";
import { TerminalQueryFilter } from "./queries";

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

function through(chunks: string[]): string {
  const filter = new TerminalQueryFilter();
  let out = "";
  for (const c of chunks) out += dec(filter.push(enc(c)));
  out += dec(filter.flush());
  return out;
}

describe("TerminalQueryFilter", () => {
  it("drops the DA1 and DA2 replies xterm.js queues for an agent query", () => {
    expect(through(["\u001b[?1;2c"])).toBe("");
    expect(through(["\u001b[>0;95;0c"])).toBe("");
    expect(through(["\u001b[c\u001b[>c"])).toBe("");
  });

  it("drops DECRQM mode reports", () => {
    expect(through(["\u001b[?2026;1$p"])).toBe("");
    expect(through(["\u001b[2026;1$p"])).toBe("");
  });

  it("drops the cursor position report", () => {
    expect(through(["\u001b[12;40R"])).toBe("");
  });

  it("drops focus in and focus out events", () => {
    expect(through(["\u001b[I\u001b[O"])).toBe("");
  });

  it("drops in-band resize reports", () => {
    expect(through(["\u001b[48;24;80;600;800t"])).toBe("");
    expect(through(["\u001b[4;24;80t"])).toBe("");
  });

  it("keeps ordinary typed text and non-query control sequences", () => {
    expect(through(["hello", "\r"])).toBe("hello\r");
    expect(through(["\u0003"])).toBe("\u0003"); // Ctrl-C stays the attached process's business
    expect(through(["\u001b[A"])) .toBe("\u001b[A"); // arrow up
    expect(through(["\u001b[1;5C"])).toBe("\u001b[1;5C"); // Ctrl-right
    expect(through(["\u001b[3~"])).toBe("\u001b[3~"); // delete
    expect(through(["\u001b[32m"])).toBe("\u001b[32m"); // SGR colour, not a reply
    expect(through(["\u001b[200~"])).toBe("\u001b[200~"); // bracketed paste start
  });

  it("keeps a resize request that happens to end in t with another parameter", () => {
    expect(through(["\u001b[8;24;80t"])).toBe("\u001b[8;24;80t");
  });

  it("reassembles a reply split across two onData events", () => {
    expect(through(["\u001b[?1;", "2c"])).toBe("");
    expect(through(["ab\u001b[?1", ";2c", "cd"])).toBe("abcd");
  });

  it("passes OSC and DCS payloads through untouched", () => {
    const osc = "\u001b]0;a [title] with \u001b inside\u0007";
    expect(through([osc])).toBe(osc);
    const dcs = "\u001bP1$r0m\u001b\\";
    expect(through([dcs])).toBe(dcs);
  });

  it("does not stall forever on an escape that never completes", () => {
    const filter = new TerminalQueryFilter();
    const noise = "\u001b[" + "1".repeat(5000);
    const out = dec(filter.push(enc(noise)));
    expect(out).toBe(noise);
    expect(filter.pending).toBe(0);
  });
});
