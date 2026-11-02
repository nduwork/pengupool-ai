import { describe, expect, it } from "vitest";

import { isShiftEnter, macOptionKeys, shellQuote, shiftEnter } from "./TerminalPane";

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
