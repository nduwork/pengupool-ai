import { describe, expect, it } from "vitest";

import type { Team } from "../protocol/domain";
import { estimateBudget, spent } from "./Teams";

const team = (state: string, cost_by?: Record<string, number>): Team => ({ id: state, name: "", harness: "claude", members: [], state, ios_access: false, password_set: false, cost_by, created_at: "" });

describe("budget estimate", () => {
  it("is the median spend per member on closed teams, times the members now", () => {
    const teams = [team("closed", { a: 1, b: 3 }), team("closed", { c: 2 }), team("active", { d: 100 })];
    expect(spent(teams[0])).toBe(4);
    expect(estimateBudget(teams, 3)).toEqual({ usd: 6, basis: 2 });
  });
  it("has no estimate before a closed team reported spend", () => {
    expect(estimateBudget([team("active", { a: 5 })], 2)).toBeNull();
  });
});
