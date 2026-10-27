import { describe, expect, it } from "vitest";

import type { SessionView } from "../protocol/control";
import { isUnread, markViewed } from "./lanes";

const s = (name: string, status: string, extra: object = {}): SessionView => ({ id: name, name, state: "running", status, harness: "claude", ...extra }) as SessionView;

describe("unread", () => {
  it("is output after the last view; a first sighting is read", () => {
    const x = s("a", "working", { active_at: "2026-10-23T10:00:05Z" });
    expect(isUnread(x, { a: "2026-10-23T10:00:00Z" })).toBe(true);
    expect(isUnread(x, { a: "2026-10-23T10:00:09Z" })).toBe(false);
    expect(isUnread(x, {})).toBe(false);
  });

  it("marks the selected session viewed, seeds new ones, drops gone ones, and keeps the object when nothing changed", () => {
    const v = markViewed({ gone: "t0", a: "t0" }, [s("a", "idle"), s("b", "idle")], "a", "t1");
    expect(v).toEqual({ a: "t1", b: "t1" });
    expect(markViewed(v, [s("a", "idle"), s("b", "idle")], null, "t2")).toBe(v);
  });
});
