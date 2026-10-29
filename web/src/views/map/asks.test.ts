import { describe, expect, it } from "vitest";

import type { TeamMessage } from "../../protocol/domain";
import { age, askPill, newestByPair, openAsks } from "./asks";

const at = "2026-10-29T10:00:00Z";
const t0 = Date.parse(at);
const msg = (m: Partial<TeamMessage>): TeamMessage => ({ id: "x", team_id: "t", from: "web", to: "lead", text: "", state: "delivered", at, ...m });

describe("open asks", () => {
  it("is open until the recipient answers, or it is settled or failed", () => {
    const ask = msg({ id: "a", type: "ask" });
    expect(openAsks([ask])).toEqual([ask]);
    expect(openAsks([ask, msg({ id: "b", from: "plan", to: "web" })])).toEqual([ask]);
    expect(openAsks([ask, msg({ id: "b", from: "lead", to: "web" })])).toEqual([]);
    expect(openAsks([ask, msg({ id: "b", from: "", to: "web", reply_to: "a" })])).toEqual([]);
    expect(openAsks([msg({ id: "b", from: "lead", to: "web" }), ask])).toEqual([ask]);
    expect(openAsks([{ ...ask, settled_at: at }])).toEqual([]);
    expect(openAsks([{ ...ask, state: "failed" }])).toEqual([]);
    expect(openAsks([msg({ id: "c" })])).toEqual([]);
  });
});

describe("ask pills", () => {
  it("shows the age, rounded down", () => {
    expect(age(at, t0 + 30_000)).toBe("now");
    expect(age(at, t0 + 4 * 60_000 + 59_000)).toBe("4m");
    expect(age(at, t0 + 61 * 60_000)).toBe("1h");
  });
  it("shows the count and the oldest age, and ↑ once escalated", () => {
    const older = msg({ id: "a", type: "ask", at: "2026-10-29T09:42:00Z" });
    expect(askPill([msg({ type: "ask" })], t0 + 4 * 60_000)).toEqual({ text: "? 4m", escalated: false });
    expect(askPill([msg({ type: "ask" }), older], t0)).toEqual({ text: "? 2 · 18m", escalated: false });
    expect(askPill([{ ...older, escalated_at: at }], t0)).toEqual({ text: "? 18m ↑", escalated: true });
  });
});

describe("queued routes", () => {
  it("keeps the newest message per direction", () => {
    const m = newestByPair([msg({ id: "1", state: "delivered" }), msg({ id: "2", state: "queued" }), msg({ id: "3", from: "", to: "lead" })]);
    expect(m.get("web>lead")?.id).toBe("2");
    expect(m.size).toBe(1);
  });
});
