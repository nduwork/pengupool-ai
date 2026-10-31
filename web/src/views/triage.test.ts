import { describe, expect, it } from "vitest";

import { triagePrompt } from "./triage";

describe("triagePrompt", () => {
  it("asks for a triage model while it is missing or not answering, and is quiet once it works", () => {
    expect(triagePrompt({ status: "missing" })).toContain("Set up a triage model");
    expect(triagePrompt({ status: "unreachable", error: "connection refused" })).toContain("not answering (connection refused)");
    expect(triagePrompt({ status: "ok" })).toBeNull();
  });
});
