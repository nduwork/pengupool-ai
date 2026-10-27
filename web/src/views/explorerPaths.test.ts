import { describe, expect, it } from "vitest";

import { QUEUE_TTL_MS, absPath, badName, joinRel, onTypePath, parentDir, safePath, takePaths, typePath } from "./explorerPaths";

describe("explorer paths", () => {
  it("splits and joins relative paths", () => {
    expect(parentDir("a/b/c.ts")).toBe("a/b");
    expect(parentDir("c.ts")).toBe("");
    expect(joinRel("", "c.ts")).toBe("c.ts");
    expect(joinRel("a/b", "c.ts")).toBe("a/b/c.ts");
  });
  it("makes absolute paths from the session's folder", () => {
    expect(absPath("/work/app", "src/x.ts")).toBe("/work/app/src/x.ts");
    expect(absPath("/work/app/", "x.ts")).toBe("/work/app/x.ts");
    expect(absPath("/work/app", "")).toBe("/work/app");
  });
  it("refuses names that are not one file name", () => {
    for (const n of ["", "  ", "a/b", ".", ".."]) expect(badName(n)).toBe(true);
    expect(badName("notes.md")).toBe(false);
  });
});

describe("typePath", () => {
  it("waits for a terminal, then hands the paths over once, in order", () => {
    typePath("s1", "/w/a.ts");
    typePath("s1", "/w/b.ts");
    expect(takePaths("s1")).toEqual(["/w/a.ts", "/w/b.ts"]);
    expect(takePaths("s1")).toEqual([]);
  });
  it("nudges a mounted terminal of that session only, until it is withdrawn", () => {
    let nudged = 0;
    const off = onTypePath("s2", () => nudged++);
    typePath("s2", "/w/a.ts");
    typePath("s3", "/w/b.ts");
    expect(nudged).toBe(1);
    off();
    typePath("s2", "/w/c.ts");
    expect(nudged).toBe(1);
    expect(takePaths("s2")).toEqual(["/w/a.ts", "/w/c.ts"]);
    takePaths("s3");
  });
  it("keeps a newer terminal's nudge when an older one withdraws late", () => {
    let a = 0;
    let b = 0;
    const offA = onTypePath("s4", () => a++);
    const offB = onTypePath("s4", () => b++);
    offA();
    typePath("s4", "/w/x");
    expect([a, b]).toEqual([0, 1]);
    offB();
    takePaths("s4");
  });
});

describe("a dragged path with control characters", () => {
  const evil = "/w/x\x1b[201~\rrm -rf ~\r.txt";
  it("is refused: it would end a bracketed paste and run as keystrokes", () => {
    expect(safePath(evil)).toBe(false);
    for (const c of ["\x00", "\x1b", "\r", "\n", "\x7f", "\x85", "\x9b"]) expect(safePath(`/w/a${c}b`)).toBe(false);
    expect(safePath("/w/my notes (1) é.md")).toBe(true);
    expect(safePath("")).toBe(false);
  });
  it("is never queued, so nothing is nudged or typed", () => {
    let nudged = 0;
    const off = onTypePath("e1", () => nudged++);
    expect(typePath("e1", evil)).toBe(false);
    expect(nudged).toBe(0);
    expect(takePaths("e1")).toEqual([]);
    off();
  });
});

describe("an expired path is reported", () => {
  it("counts what ran out and still types what is fresh", () => {
    typePath("x1", "/w/old", 0);
    typePath("x1", "/w/new", QUEUE_TTL_MS + 10);
    let n = 0;
    expect(takePaths("x1", QUEUE_TTL_MS + 11, (k) => (n = k))).toEqual(["/w/new"]);
    expect(n).toBe(1);
  });
});

describe("the queue expires", () => {
  it("drops a path older than the TTL, so it is not typed into a later attach", () => {
    typePath("x1", "/w/old.ts", 1000);
    typePath("x1", "/w/new.ts", 1000 + QUEUE_TTL_MS);
    expect(takePaths("x1", 1000 + QUEUE_TTL_MS + 1)).toEqual(["/w/new.ts"]);
    typePath("x2", "/w/a.ts", 5000);
    expect(takePaths("x2", 5000 + QUEUE_TTL_MS)).toEqual(["/w/a.ts"]);
  });
});
