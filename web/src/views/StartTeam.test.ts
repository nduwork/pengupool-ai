import { describe, expect, it } from "vitest";

import type { Skill } from "../protocol/domain";
import { matching, suggested } from "./StartTeam";

const sk = (name: string, extra: Partial<Skill> = {}): Skill => ({ id: "id-" + name, name, path: "/p/" + name, source: "git", ...extra }) as Skill;

describe("Start with a helper", () => {
  it("suggests the installed common helpers in their fixed order, leaving out the rest", () => {
    const installed = [sk("ios-app-agent"), sk("pr-review-agent"), sk("git-runner-agent"), sk("research-agent"), sk("data-analysis-agent")];
    expect(suggested(installed).map((s) => s.name)).toEqual(["research-agent", "pr-review-agent", "data-analysis-agent"]);
  });
  it("searches every helper by name, description and capability", () => {
    const all = [sk("ios-app-agent", { description: "Ships a SwiftUI app to TestFlight" }), sk("git-ci-agent", { capabilities: ["ci-debug"] }), sk("research-agent")];
    expect(matching(all, "testflight").map((s) => s.name)).toEqual(["ios-app-agent"]);
    expect(matching(all, "CI-DEBUG").map((s) => s.name)).toEqual(["git-ci-agent"]);
    expect(matching(all, "  ")).toHaveLength(3);
  });
});
