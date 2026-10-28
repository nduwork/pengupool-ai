import { describe, expect, it } from "vitest";

import { SCROLLBACK_DEFAULT, SCROLLBACK_MAX, SCROLLBACK_MIN, TermCache, clampScrollback } from "./termCache";

describe("TermCache", () => {
  it("hands a kept terminal back once, and disposes the least recently left past the limit", () => {
    const gone: string[] = [];
    const c = new TermCache<string>(2, (v) => gone.push(v));
    c.put("a", "A");
    c.put("b", "B");
    expect(c.take("a")).toBe("A");
    expect(c.take("a")).toBeUndefined();
    c.put("a", "A");
    c.put("c", "C"); // b is the least recently left
    expect(gone).toEqual(["B"]);
    expect(c.size).toBe(2);
  });

  it("disposes a replaced terminal and a dropped one", () => {
    const gone: string[] = [];
    const c = new TermCache<string>(3, (v) => gone.push(v));
    c.put("a", "A1");
    c.put("a", "A2");
    c.drop("a");
    expect(gone).toEqual(["A1", "A2"]);
    expect(c.size).toBe(0);
  });
});

describe("clampScrollback", () => {
  it("keeps the setting between the limits and falls back for nonsense", () => {
    expect(clampScrollback(50)).toBe(SCROLLBACK_MIN);
    expect(clampScrollback(10 ** 9)).toBe(SCROLLBACK_MAX);
    expect(clampScrollback(NaN)).toBe(SCROLLBACK_DEFAULT);
    expect(clampScrollback(30000.4)).toBe(30000);
  });
});
