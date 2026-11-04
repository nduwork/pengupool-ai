import { describe, expect, it } from "vitest";

import type { SessionView } from "../protocol/control";
import type { Approval, TeamMessage } from "../protocol/domain";
import { approvalTitle, askNeeds, dismissKey, sessionNeeds, type OpenGate, waitingOnOwner } from "./inbox";

const s = (name: string, status: string, state = "running"): SessionView => ({ id: name, name, state, status, harness: "claude" }) as SessionView;

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

describe("waitingOnOwner", () => {
  const ap = (id: string, kind: string, state = "pending", session_id?: string): Approval => ({ id, kind, state, detail: id, session_id, created_at: "" }) as Approval;
  it("is one list: pending approvals (not permissions; an agent's ask while it runs), then each session's need", () => {
    const sessions = [s("a", "approval"), s("b", "idle"), { ...s("c", "working"), state: "exited" } as SessionView];
    const w = waitingOnOwner([ap("L", "launch"), ap("P", "permission"), ap("Q", "ask", "pending", "b"), ap("X", "ask", "pending", "c"), ap("D", "launch", "approved")], sessions, [], []);
    expect(w.items.map((i) => i.key)).toEqual(["approval:L", "approval:Q", "need:a:permission"]);
    expect(w.items.map((i) => i.title)).toEqual(["Team launch ready", "An agent asks you", "a needs you"]);
  });
  it("counts a go-ahead to check, even when nothing else waits", () => {
    const w = waitingOnOwner([], [s("a", "idle")], [], [], [{ id: 7, agent: "a", question: "Ran the migration" }]);
    expect(w.items.map((i) => [i.key, i.title])).toEqual([["spot:7", "Was this go-ahead right?"]]);
  });
  it("hides a dismissed item, still waiting, until its words change; the others stay", () => {
    const spot = { id: 7, agent: "a", question: "Ran the migration" };
    const all = waitingOnOwner([ap("L", "launch")], [s("a", "approval")], [], [], [spot]);
    expect(all.items.map((i) => i.key)).toEqual(["approval:L", "need:a:permission", "spot:7"]);
    const gone = new Set(all.items.map(dismissKey));
    const none = waitingOnOwner([ap("L", "launch")], [s("a", "approval")], [], [], [spot], gone);
    expect([none.items, none.approvals, none.needs, none.spots]).toEqual([[], [], [], []]);
    // One dismissed: only it goes, from the rows and from the count alike.
    const one = waitingOnOwner([ap("L", "launch")], [s("a", "approval")], [], [], [spot], new Set([dismissKey(all.items[0]!)]));
    expect(one.items.map((i) => i.key)).toEqual(["need:a:permission", "spot:7"]);
    expect([one.approvals.length, one.needs.length, one.spots.length]).toEqual([0, 1, 1]);
    // It changes: a new permission call, a new go-ahead question, the session now on a check-in. Each comes back.
    const perm: OpenGate = { id: 1, session_id: "a", agent: "a", kind: "permission", question: "Bash: rm x", floor: false };
    const back = waitingOnOwner([{ ...ap("L", "launch"), detail: "L, revised" }], [s("a", "approval")], [], [perm], [{ ...spot, question: "Ran it again" }], gone);
    expect(back.items.map((i) => i.key)).toEqual(["approval:L", "need:a:permission", "spot:7"]);
    const checkin: OpenGate = { id: 2, session_id: "a", agent: "a", kind: "checkin", question: "Go on?", floor: false };
    expect(waitingOnOwner([], [s("a", "idle")], [], [checkin], [], gone).items.map((i) => i.key)).toEqual(["need:a:checkin"]);
  });
  it("drops an item as soon as the state says it was answered, wherever that was", () => {
    const before = waitingOnOwner([ap("Q", "ask", "pending", "b")], [s("a", "approval"), s("b", "idle")], [], []);
    const after = waitingOnOwner([ap("Q", "ask", "answered", "b")], [s("a", "working"), s("b", "idle")], [], []);
    expect([before.items.length, after.items.length]).toEqual([2, 0]);
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
  it("keeps a check-in and an ask from one session apart in the one list", () => {
    const gate: OpenGate = { id: 1, session_id: "a", agent: "a", kind: "checkin", question: "Shall I go on?", floor: false };
    const keys = waitingOnOwner([], [s("a", "idle"), s("b", "idle")], [ask("1", { escalated_at: "x" })], [gate]).items.map((i) => i.key);
    expect(keys).toEqual(["need:a:checkin", "need:ask:1:ask"]);
  });
});
