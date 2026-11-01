import { describe, expect, it } from "vitest";

import { stepsOf } from "./checklist";

describe("stepsOf", () => {
  it("detects each step from the setup, with its value or why it matters", () => {
    const none = stepsOf({ agent: "", decision: "", permissions: "" });
    expect(none.map((x) => x.done)).toEqual([false, false, false]);
    expect(none.map((x) => x.detail)).toEqual(["No agent installed", "Required for memory and triage", "Not decided"]);
    const set = stepsOf({ agent: "Codex 0.166.0", decision: "Local hosted · tev1:0.8b", permissions: "off" });
    expect(set.map((x) => x.done)).toEqual([true, true, true]);
    expect(set.map((x) => x.detail)).toEqual(["Codex 0.166.0", "Local hosted · tev1:0.8b", "Asking each time"]);
  });
});
