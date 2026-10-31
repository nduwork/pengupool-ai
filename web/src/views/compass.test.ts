import { describe, expect, it } from "vitest";

import type { PerCompass } from "../protocol/domain";
import { groupLines, ownerViaLabel, perCompassLabel, type Line } from "./compass";

const l = (uid: string, state: Line["state"], mu: number): Line => ({ uid, state, mu, kind: "preference", body: uid, created_at: "", n: 0, sigma: 0, effects: 0 });

describe("groupLines", () => {
  const lines = [l("a", "learning", 0.4), l("b", "confident", 0.7), l("c", "confident", 0.9), l("e", "fading", 0.5)];
  it("groups by state, strongest first", () => {
    const out = groupLines(lines);
    expect(out.map((g) => g.title)).toEqual(["Guiding", "Learning", "Fading"]);
    expect(out[0].lines.map((x) => x.uid)).toEqual(["c", "b"]);
  });
});

describe("per-Compass labels", () => {
  const name = (id: string): string => ({ s1: "lead-a", s2: "helper-b" })[id] ?? id;
  const rec = (o: Partial<PerCompass>): PerCompass => ({ item: "a1", team_id: "t", session_id: "s2", lead: "s1", question: "q", outcome: "answered", detail: "yes", version: "v", at: "", ...o });
  it("names the line it followed, by text when the Compass has it", () => {
    expect(perCompassLabel(rec({ line_uid: "u1" }), name, [l("u1", "confident", 0.9)])).toBe("decided by lead-a per your line: u1");
    expect(perCompassLabel(rec({ line_uid: "zz" }), name, [])).toBe("decided by lead-a per your line: line zz");
  });
  it("says why a refusal refused", () => {
    expect(perCompassLabel(rec({ outcome: "refused", detail: "not sure" }), name, [])).toBe("lead-a checked the Compass: not sure");
  });
  it("labels the owner's words via a session, nothing else", () => {
    expect(ownerViaLabel("owner via s2", name)).toBe("your words via helper-b");
    expect(ownerViaLabel("owner", name)).toBeUndefined();
    expect(ownerViaLabel(undefined, name)).toBeUndefined();
  });
});
