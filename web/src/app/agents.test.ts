import { describe, expect, it } from "vitest";

import { launchAgent, pickable, type CustomAgent, type Found } from "./ctx";

const yes = { version: "1.0" };
const no = { version: "", error: "harness_unavailable: not on PATH" };

describe("New session's agent list", () => {
  it("offers installed agents only, then Custom agents", () => {
    const found: Found = { claude: yes, muse: no, codex: yes, pi: no, omp: yes, opencode: no };
    const customs: CustomAgent[] = [
      { id: "o", name: "Claude on Ollama", command: "ollama launch claude --", starts: "claude" },
      { id: "p", name: "pi on box", command: "ssh-pi --", starts: "pi" }, // starts an agent that is not installed
      { id: "t", name: "htop", command: "htop" },
    ];
    expect(pickable(found, customs).map((o) => o.value)).toEqual(["claude", "codex", "omp", "custom:o", "custom:t"]);
  });
  it("never offers a hidden agent, even when installed", () => {
    expect(pickable({ claude: yes, copilot: yes, gemini: yes }).map((o) => o.value)).toEqual(["claude"]);
  });
  it("offers nothing until the install check answers", () => {
    expect(pickable(null, [{ id: "t", name: "htop", command: "htop" }]).map((o) => o.value)).toEqual(["custom:t"]);
  });
  it("launches a Custom agent by its id", () => {
    expect(launchAgent("custom:o", "")).toEqual({ harness: "", custom: "o", model: undefined });
    expect(launchAgent("codex", " gpt-5 ")).toEqual({ harness: "codex", model: "gpt-5" });
  });
});
