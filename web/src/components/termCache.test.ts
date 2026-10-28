import { describe, expect, it } from "vitest";

import { SCROLLBACK_DEFAULT, SCROLLBACK_MAX, SCROLLBACK_MIN, TermCache, clampScrollback, instanceOf } from "./termCache";

type T = { instance: string; name: string };
const t = (name: string, instance = "e|1|s"): T => ({ name, instance });

describe("TermCache", () => {
  it("hands a kept terminal back once, and disposes the least recently left past the limit", () => {
    const gone: string[] = [];
    const c = new TermCache<T>(2, (v) => gone.push(v.name));
    c.put("a", t("A"));
    c.put("b", t("B"));
    expect(c.take("a", "e|1|s")?.name).toBe("A");
    expect(c.take("a", "e|1|s")).toBeUndefined();
    c.put("a", t("A"));
    c.put("c", t("C")); // b is the least recently left
    expect(gone).toEqual(["B"]);
    expect(c.size).toBe(2);
  });

  it("does not resume a terminal kept from another process: a resumed session, a restarted daemon", () => {
    const gone: string[] = [];
    const c = new TermCache<T>(3, (v) => gone.push(v.name));
    c.put("a", t("old", instanceOf("epoch1", { pid: 100, started_at: "t1" })));
    // session.resume: same id, new worker, new pid and start; the old scrollback is not replayed onto.
    expect(c.take("a", instanceOf("epoch1", { pid: 200, started_at: "t2" }))).toBeUndefined();
    expect(gone).toEqual(["old"]);
    c.put("a", t("old2", instanceOf("epoch1", { pid: 200, started_at: "t2" })));
    // daemon restart: a new epoch, even with the same pid and start.
    expect(c.take("a", instanceOf("epoch2", { pid: 200, started_at: "t2" }))).toBeUndefined();
    c.put("a", t("same", instanceOf("epoch2", { pid: 200, started_at: "t2" })));
    expect(c.take("a", instanceOf("epoch2", { pid: 200, started_at: "t2" }))?.name).toBe("same");
  });

  it("disposes a replaced terminal and those of sessions that are gone", () => {
    const gone: string[] = [];
    const c = new TermCache<T>(5, (v) => gone.push(v.name));
    c.put("a", t("A1"));
    c.put("a", t("A2"));
    c.put("b", t("B"));
    c.prune(new Set(["b"]));
    expect(gone).toEqual(["A1", "A2"]);
    expect(c.size).toBe(1);
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
