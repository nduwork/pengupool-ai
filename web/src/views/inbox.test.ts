import { describe, expect, it } from "vitest";

import type { SessionView } from "../protocol/control";
import type { TeamMessage } from "../protocol/domain";
import { approvalTitle, ARCHIVE_MS, askNeeds, reconcileInbox, sessionNeeds, type Need, type OpenGate } from "./inbox";

const s = (name: string, status: string, state = "running"): SessionView => ({ id: name, name, state, status, harness: "claude" }) as SessionView;
const need = (session: string, text = "waiting on a permission prompt"): Need => ({ session, name: session, kind: "permission", text });

describe("sessionNeeds", () => {
  it("lists running agents blocked on a prompt or a lost connection, never finished ones", () => {
    const out = sessionNeeds([s("a", "approval"), s("b", "working"), s("c", "idle"), s("d", "lost"), s("e", "approval", "exited")], []);
    expect(out.map((n) => [n.session, n.kind])).toEqual([["a", "permission"], ["d", "block"]]);
  });

  it("names the tool call of a permission prompt and the question of a check-in, one ask per session", () => {
    const g = (id: number, session_id: string, kind: "checkin" | "permission", question: string): OpenGate => ({ id, session_id, agent: session_id, kind, question, floor: false });
    const out = sessionNeeds([s("a", "approval"), s("b", "idle"), s("c", "working")], [g(1, "a", "permission", "Bash: git push origin main"), g(2, "b", "checkin", "Shall I go on?"), g(3, "c", "checkin", "stale")]);
    expect(out.map((n) => [n.session, n.text, n.gate?.id])).toEqual([["a", "Approve Bash: git push origin main?", 1], ["b", "Shall I go on?", 2]]);
  });
});

describe("sessionNeeds first-run dialogs", () => {
  it("lists an agent stuck on a first-run dialog while starting (trust, login, setup)", () => {
    const stuck = { ...s("a", "starting"), waiting: "trust this folder" } as SessionView;
    const out = sessionNeeds([stuck, s("b", "starting")], []);
    expect(out.map((n) => [n.session, n.kind, n.text])).toEqual([["a", "block", "is waiting on a first-run dialog (trust this folder): answer it in its terminal"]]);
  });
});

describe("approvalTitle", () => {
  it("titles an agent's question and a team change by what they are, not as a team launch", () => {
    expect([approvalTitle("ask"), approvalTitle("formation"), approvalTitle("launch")]).toEqual(["An agent asks you", "Team change to approve", "Team launch ready"]);
  });
});

describe("reconcileInbox", () => {
  it("merges a session's asks into one entry and keeps its first time while the ask stands", () => {
    const one = reconcileInbox([], [need("a")], 100);
    expect(reconcileInbox(one, [need("a")], 200)).toEqual(one);
    expect(reconcileInbox(one, [need("a", "is lost")], 200)).toEqual([{ ...need("a", "is lost"), at: 200 }]);
  });

  it("resolves a gone ask, then archives it after a day", () => {
    const one = reconcileInbox([], [need("a")], 100);
    const done = reconcileInbox(one, [], 200);
    expect(done[0].resolvedAt).toBe(200);
    expect(reconcileInbox(done, [], 300)[0].resolvedAt).toBe(200);
    expect(reconcileInbox(done, [], 200 + ARCHIVE_MS)).toEqual([]);
  });

  it("keeps a block open while it is still live", () => {
    const blocked: Need = { session: "a", name: "a", kind: "block", text: "is lost" };
    const e = reconcileInbox([], [blocked], 0);
    expect(reconcileInbox(e, [blocked], 5 * ARCHIVE_MS)[0].resolvedAt).toBeUndefined();
  });
  it("leads a permission prompt with the agent's description and the floor item, the whole call kept as detail", () => {
    const long = "python3 - <<'EOF'; " + "x = 1; ".repeat(40) + "EOF";
    const g = (description: string | undefined, question: string, floor_hit?: string): OpenGate => ({ id: 1, session_id: "a", agent: "a", kind: "permission", question, description, floor: floor_hit !== undefined, floor_hit });
    const one = (gate: OpenGate) => sessionNeeds([s("a", "approval")], [gate])[0];
    expect(one(g("Check installed PenguPool version", "Bash: P=/x/pengupool; $P version", "shell indirection"))).toMatchObject({
      text: "Approve Bash: Check installed PenguPool version (shell indirection)?",
      detail: "Bash: P=/x/pengupool; $P version",
    });
    const fallback = one(g(undefined, "Bash: " + long));
    expect(fallback.text).toBe("Approve Bash: python3 - <<'EOF'?");
    expect(fallback.detail).toBe("Bash: " + long);
    expect(one(g(undefined, "Bash: " + "a".repeat(200))).text.length).toBeLessThan(100);
  });
});

describe("askNeeds", () => {
  const ask = (id: string, m: Partial<TeamMessage> = {}): TeamMessage => ({ id, team_id: "t", from: "a", to: "b", text: "which port?", state: "delivered", at: "2026-10-29T10:00:00Z", type: "ask", ...m });
  it("lists only escalated open asks whose asker still runs", () => {
    const out = askNeeds([ask("1", { escalated_at: "x" }), ask("2"), ask("3", { escalated_at: "x", settled_at: "y" }), ask("4", { escalated_at: "x", from: "gone" })], [s("a", "idle"), s("b", "working"), s("gone", "idle", "exited")]);
    expect(out.map((n) => [n.key, n.name, n.toName])).toEqual([["ask:1", "a", "b"]]);
  });
  it("keeps a check-in and an ask from one session apart in the inbox", () => {
    const checkin: Need = { session: "a", name: "a", kind: "checkin", text: "Shall I go on?" };
    const [askNeed] = askNeeds([ask("1", { escalated_at: "x" })], [s("a", "idle"), s("b", "idle")]);
    const out = reconcileInbox([], [checkin, askNeed], 100);
    expect(out.map((e) => e.kind).sort()).toEqual(["ask", "checkin"]);
    expect(reconcileInbox(out, [checkin], 200).find((e) => e.kind === "ask")?.resolvedAt).toBe(200);
  });
});
