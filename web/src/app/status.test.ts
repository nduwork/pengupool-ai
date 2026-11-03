import { describe, expect, it } from "vitest";

import { shownStatus, statusStyle } from "./ctx";
import { sessionNeeds, type OpenGate } from "../views/inbox";
import type { SessionView } from "../protocol/control";

const s = (id: string, status: string, state = "running"): SessionView => ({ id, name: id, harness: "claude", cwd: "/w", status, state }) as SessionView;
const checkin = (id: string): OpenGate => ({ id: 1, session_id: id, kind: "checkin", question: "Ship it?" }) as OpenGate;

describe("? Need input", () => {
  it("shows on a session waiting on a permission prompt or a check-in, in red with a ?", () => {
    expect(shownStatus(s("a", "approval"), [])).toBe("approval");
    expect(shownStatus(s("b", "idle"), [checkin("b")])).toBe("approval"); // was "Waiting"
    expect(statusStyle("approval")).toMatchObject({ label: "? Need input", dot: "bg-danger", text: "text-danger" });
  });
  it("not while the session works, is gone, or the check-in is another session's", () => {
    expect(shownStatus(s("c", "working"), [checkin("c")])).toBe("working");
    expect(shownStatus(s("d", "idle", "ended"), [checkin("d")])).toBe("ended");
    expect(shownStatus(s("e", "idle"), [checkin("x")])).toBe("idle");
    expect(statusStyle("idle").label).toBe("Waiting");
  });
  it("shows while the agent waits on its ask to the owner, even though it reads working", () => {
    const ask = (id: string, state = "pending", kind = "ask") => ({ id: "ap-" + id, session_id: id, kind, state });
    expect(shownStatus(s("f", "working"), [], [ask("f")])).toBe("approval");
    expect(shownStatus(s("g", "idle"), [], [ask("g")])).toBe("approval");
    expect(shownStatus(s("h", "working"), [], [ask("h", "answered")])).toBe("working");
    expect(shownStatus(s("i", "working"), [], [ask("x")])).toBe("working");
    expect(shownStatus(s("j", "working"), [], [ask("j", "pending", "launch")])).toBe("working");
    expect(shownStatus(s("k", "idle", "ended"), [], [ask("k")])).toBe("ended");
    expect(shownStatus(s("l", "lost"), [], [ask("l")])).toBe("lost");
  });
  it("matches what the Needs-you bar lists", () => {
    const sessions = [s("a", "approval"), s("b", "idle"), s("c", "working"), s("e", "idle")];
    const gates = [checkin("b"), checkin("c")];
    const needs = sessionNeeds(sessions, gates).map((n) => n.session);
    expect(sessions.filter((x) => shownStatus(x, gates) === "approval").map((x) => x.id)).toEqual(needs);
  });
});
