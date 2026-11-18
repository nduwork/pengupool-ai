import { describe, expect, it } from "vitest";

import { agentsRunning, restartActive, restartLabel, restartPlan, restartSummary, type RestartItem } from "./restartAll";

const item = (state: string): RestartItem => ({ session_id: state, name: state, harness: "claude", state });
const agent = (s: { harness?: string }): boolean => s.harness !== "shell";

describe("restartAll", () => {
  it("labels each state", () => {
    expect(restartLabel("resumed")).toEqual({ label: "resumed", tone: "success" });
    expect(restartLabel("fresh").label).toBe("restarted fresh");
    expect(restartLabel("queued").label).toBe("waits its turn");
    expect(restartLabel("queued", true).label).toBe("waits for its turn to end");
    expect(restartLabel("failed").tone).toBe("danger");
  });
  it("is active while something is queued or restarting", () => {
    expect(restartActive([item("resumed"), item("queued")])).toBe(true);
    expect(restartActive([item("restarting")])).toBe(true);
    expect(restartActive(["resumed", "fresh", "failed", "cancelled"].map(item))).toBe(false);
  });
  it("sums up a run", () => {
    expect(restartSummary(["queued", "resumed", "fresh", "resumed"].map(item))).toBe("2 resumed · 1 restarted fresh · 1 waiting");
    expect(restartSummary([])).toBe("No agent session is running.");
  });
  it("sees whether an agent session runs", () => {
    expect(agentsRunning([], agent)).toBe(false);
    expect(agentsRunning([{ id: "a", state: "running", harness: "shell" }, { id: "b", state: "exited", harness: "claude" }], agent)).toBe(false);
    expect(agentsRunning([{ id: "c", state: "running", harness: "codex" }], agent)).toBe(true);
  });
  it("plans the running agents, marking the busy ones", () => {
    const plan = restartPlan(
      [
        { id: "1", state: "running", harness: "claude", status: "idle" },
        { id: "2", state: "running", harness: "codex", status: "working" },
        { id: "3", state: "running", harness: "claude", status: "approval" },
        { id: "4", state: "running", harness: "omp", status: "starting" },
        { id: "5", state: "exited", harness: "claude", status: "ended" },
        { id: "6", state: "running", harness: "shell", status: "idle" },
      ],
      agent,
    );
    expect(plan.map((e) => [e.s.id, e.waits])).toEqual([["1", false], ["2", true], ["3", true], ["4", true]]);
  });
});
