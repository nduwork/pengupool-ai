import { describe, expect, it } from "vitest";

import { checkFailed, choosePick, launchAgent, parentPick, pickable, type CustomAgent, type Found } from "./ctx";

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
  it("a failed install check blocks nothing", () => {
    expect(pickable(checkFailed()).map((o) => o.value)).toEqual(["claude", "muse", "codex", "pi", "omp", "opencode"]);
  });
  it("launches a Custom agent by its id", () => {
    expect(launchAgent("custom:o", "")).toEqual({ harness: "", custom: "o", model: undefined });
    expect(launchAgent("codex", " gpt-5 ")).toEqual({ harness: "codex", model: "gpt-5" });
  });
  it("a child starts on its parent's Custom agent, else its agent", () => {
    expect(parentPick({ harness: "claude", custom: "o" })).toBe("custom:o");
    expect(parentPick({ harness: "codex" })).toBe("codex");
    expect(parentPick({ harness: "shell" })).toBeUndefined();
  });
  it("the default Custom agent beats a pick that is not offered", () => {
    const customs: CustomAgent[] = [{ id: "o", name: "Ollama", command: "ollama launch claude --", starts: "claude", default: true }];
    const options = pickable({ claude: yes, codex: yes }, customs);
    expect(choosePick(options, "", customs)).toBe("custom:o");
    expect(choosePick(options, "gone", customs)).toBe("custom:o");
    expect(choosePick(options, "codex", customs)).toBe("codex");
    expect(choosePick(pickable({ codex: yes }, customs), "", customs)).toBe("codex"); // its agent is not installed
  });
});
