import { describe, expect, it } from "vitest";

import { stepsOf } from "./checklist";

describe("stepsOf", () => {
  it("detects each step from the setup", () => {
    const done = (s: Parameters<typeof stepsOf>[0]): boolean[] => stepsOf(s).map((x) => x.done);
    expect(done({ agent: false, teams: 0, decision: false })).toEqual([false, false, false]);
    expect(done({ agent: true, teams: 2, decision: false })).toEqual([true, true, false]);
  });
});
