import { describe, expect, it } from "vitest";

import { isShiftEnter, macOptionKeys, osc52Handler, osc52Text, shellQuote, shiftEnter } from "./TerminalPane";

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
