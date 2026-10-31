import { describe, expect, it, vi } from "vitest";

import { type TriageStatus, triagePrompt, watchTriage } from "./triage";

describe("triagePrompt", () => {
  it("asks for a triage model while it is missing or not answering, and is quiet once it works", () => {
    expect(triagePrompt({ status: "missing" })).toContain("Set up a triage model");
    expect(triagePrompt({ status: "unreachable", error: "connection refused" })).toContain("not answering (connection refused)");
    expect(triagePrompt({ status: "ok" })).toBeNull();
  });
});

describe("watchTriage", () => {
  it("shows the prompt once an unchecked model turns out unreachable, with no other change", async () => {
    vi.useFakeTimers();
    const answers: TriageStatus[] = [{ status: "unchecked" }, { status: "unreachable", error: "connection refused" }];
    let shown: string | null = "never set";
    const stop = watchTriage(async () => answers.shift() ?? { status: "unreachable" }, (t) => (shown = triagePrompt(t)), 30_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(shown).toBeNull(); // unchecked: its check is running
    await vi.advanceTimersByTimeAsync(30_000);
    expect(shown).toContain("not answering (connection refused)");
    stop();
    vi.useRealTimers();
  });
});
