import { describe, expect, it, vi } from "vitest";

import { type TriageStatus, triagePrompt, watchTriage } from "./triage";

describe("triagePrompt", () => {
  it("asks for a triage model while it is missing or not answering, and is quiet once it works", () => {
    expect(triagePrompt({ status: "missing" })).toContain("Set up a triage model");
    expect(triagePrompt({ status: "unreachable", error: "connection refused" })).toContain("not answering (connection refused)");
    expect(triagePrompt({ status: "ok" })).toBeNull();
  });
  it("says why when the host removed the active model", () => {
    expect(triagePrompt({ status: "missing", removed: { type: "ollama", format: "openai-chat", model: "llama3.1:8b", active: true } })).toBe(
      "The local triage model (llama3.1:8b, Ollama chat format) was removed: PenguPool's triage now uses System One decision models only. Set one up in Settings.",
    );
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
