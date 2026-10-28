import { describe, expect, it } from "vitest";

import { groupLines, type Line } from "./compass";

const l = (uid: string, state: Line["state"], mu: number): Line => ({ uid, state, mu, kind: "preference", body: uid, created_at: "", n: 0, sigma: 0, effects: 0 });

describe("groupLines", () => {
  const lines = [l("a", "learning", 0.4), l("b", "confident", 0.7), l("c", "confident", 0.9), l("e", "fading", 0.5)];
  it("groups by state, strongest first", () => {
    const out = groupLines(lines);
    expect(out.map((g) => g.title)).toEqual(["Guiding", "Learning", "Fading"]);
    expect(out[0].lines.map((x) => x.uid)).toEqual(["c", "b"]);
  });
});
