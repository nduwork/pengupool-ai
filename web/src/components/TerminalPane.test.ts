import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";

import { AttachSession } from "../client/attach";
import { T, type ControlMessage } from "../protocol/control";
import { FrameType } from "../protocol/frame";

import { isShiftEnter, macOptionKeys, osc52Handler, osc52Text, shellQuote, shiftEnter, terminalSink } from "./TerminalPane";

describe("shellQuote", () => {
  it("escapes what a shell would split or expand, as Terminal.app does", () => {
    expect(shellQuote("/Users/me/a-b_c.v2/file.png")).toBe("/Users/me/a-b_c.v2/file.png");
    expect(shellQuote("/Users/me/My Shots/it's (1).png")).toBe("/Users/me/My\\ Shots/it\\'s\\ \\(1\\).png");
    expect(shellQuote("/tmp/$x&y;z")).toBe("/tmp/\\$x\\&y\\;z");
  });
});

describe("isShiftEnter", () => {
  const k = { key: "Enter", shiftKey: false, ctrlKey: false, altKey: false, metaKey: false };
  it("catches Shift+Enter only; Enter and other chords pass through to xterm", () => {
    expect(isShiftEnter({ ...k, shiftKey: true })).toBe(true);
    expect(isShiftEnter(k)).toBe(false);
    expect(isShiftEnter({ ...k, shiftKey: true, metaKey: true })).toBe(false);
    expect(isShiftEnter({ ...k, key: "a", shiftKey: true })).toBe(false);
  });
});

describe("shiftEnter", () => {
  it("types Ctrl+J for pi, whose input submits on ESC CR, and ESC CR for every other harness", () => {
    expect(shiftEnter("pi")).toBe("\n");
    for (const h of ["claude", "muse", "codex", "omp", "copilot", "gemini", "shell", "custom", undefined]) expect(shiftEnter(h)).toBe("\x1b\r");
  });
});

describe("macOptionKeys", () => {
  it("keeps Option as Meta for keys and lets Option-drag select over an agent's mouse mode", () => {
    expect(macOptionKeys).toEqual({ macOptionIsMeta: true, macOptionClickForcesSelection: true });
  });
});

describe("osc52Text", () => {
  it("decodes a clipboard write, UTF-8 included", () => {
    expect(osc52Text("c;" + btoa("hello world"))).toBe("hello world");
    expect(osc52Text(";" + btoa(String.fromCharCode(...new TextEncoder().encode("héllo ✓"))))).toBe("héllo ✓");
  });
  it("never answers a read query, and ignores empty or broken payloads", () => {
    expect(osc52Text("c;?")).toBeUndefined();
    expect(osc52Text("c;")).toBeUndefined();
    expect(osc52Text("c")).toBeUndefined();
    expect(osc52Text("c;not base64!")).toBeUndefined();
  });
});

describe("osc52Handler", () => {
  it("writes a decoded copy to the clipboard while live, and consumes every OSC 52", () => {
    const writes: string[] = [];
    let live = true;
    const h = osc52Handler(() => live, (t) => writes.push(t));
    expect(h("c;" + btoa("copied"))).toBe(true);
    expect(h("c;?")).toBe(true); // read query: consumed, not answered, no write
    live = false;
    expect(h("c;" + btoa("replayed"))).toBe(true);
    expect(writes).toEqual(["copied"]);
  });
});

describe("OSC 52 through a real attach and xterm.js", () => {
  it("does not copy what a replay redraws, though xterm.js parses it after the session is live; copies what follows", async () => {
    const term = new Terminal({ allowProposedApi: true });
    const replaying = { n: 0 };
    const copied: string[] = [];
    term.parser.registerOscHandler(52, osc52Handler(() => replaying.n === 0, (t) => copied.push(t)));
    const session = new AttachSession({
      sessionId: "s",
      channel: 1,
      sink: terminalSink(term, replaying),
      send: () => true,
      sendFrame: () => true,
      events: { state: () => {}, error: () => {}, exit: () => {} },
    });
    const output = (seq: number, text: string): number => {
      const payload = new TextEncoder().encode(text);
      session.handleFrame({ header: { type: FrameType.Output, channel: 1, seq: BigInt(seq), length: payload.length }, payload });
      return seq + payload.length;
    };
    const osc = (s: string): string => `\x1b]52;c;${btoa(s)}\x07`;

    session.attach();
    const old = osc("old copy");
    session.handleControl({ id: "r", type: T.SessionAttached, mode: "replay", generation: 1, cols: 80, rows: 24, offset: 0, end: old.length } as ControlMessage);
    const next = output(0, old); // ring bytes up to `end`: replay
    expect(session.attachState).toBe("live"); // already live before xterm.js parses the replayed copy
    output(next, osc("new copy"));
    await new Promise<void>((r) => term.write("", r));
    expect(copied).toEqual(["new copy"]);
    term.dispose();
  });
});
