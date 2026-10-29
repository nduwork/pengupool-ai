import { describe, expect, it } from "vitest";

import type { Domain, Member, Team } from "../protocol/domain";
import { leadOf, roleOf, teamLabel } from "./Formation";

const m = (id: string, x: Partial<Member> = {}): Member => ({ session_id: id, name: id, role: "helper", state: "active", ...x });
const team = (members: Member[]): Team => ({ id: "t", name: "t", harness: "claude", members, state: "active", ios_access: false, password_set: false, created_at: "" });

describe("team labels", () => {
  it("derives Lead, SME and Helper from the tree", () => {
    const t = team([
      m("root", { role: "main", repo_id: "r1", worktree: "/w" }), // a root with a repository is the Lead, not an SME
      m("sme", { role: "sme", repo_id: "r2", worktree: "/w2", parent: "root" }),
      m("help", { skill_id: "k", parent: "root" }),
      m("plain", { parent: "root" }),
    ]);
    expect(t.members.map((x) => teamLabel(t, x))).toEqual(["lead", "sme", "helper", undefined]);
    // A session the owner grouped in that works in a known repository is an SME.
    expect(teamLabel(t, t.members[3], "/src/app/sub", [{ path: "/src/app" }])).toBe("sme");
    expect(teamLabel(t, t.members[3], "/src/application", [{ path: "/src/app" }])).toBeUndefined();
  });
  it("a team with no repository member still has a Lead", () => {
    const t = team([m("a", { role: "main" }), m("b", { parent: "a" })]);
    expect(leadOf(t)).toBe("a");
    expect(roleOf({ teams: [t], repos: [] } as unknown as Domain, [], "a")).toBe("lead");
  });
  it("with no main worker, the root of the tree leads", () => {
    const t = team([m("b", { parent: "a" }), m("a", { skill_id: "hub" })]);
    expect(leadOf(t)).toBe("a");
  });
  it("a member under one that left still hangs under the root", () => {
    const t = team([m("c", { parent: "b" }), m("b", { parent: "a", state: "closed" }), m("a", { skill_id: "hub" })]);
    expect(leadOf(t)).toBe("a");
  });
});
