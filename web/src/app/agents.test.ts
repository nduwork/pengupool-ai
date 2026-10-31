import { describe, expect, it } from "vitest";

import { CLAUDE_OLLAMA, checkFailed, choosePick, pickReady, launchAgent, parentPick, pickable, AGENTS, type Found } from "./ctx";

const yes = { version: "1.0" };
const no = { version: "", error: "harness_unavailable: not on PATH" };
const ollama = { version: "/opt/homebrew/bin/ollama", model: "qwen3-coder" };

describe("New session's agent list", () => {
  it("offers installed agents only, Claude Code (Ollama) right after Claude Code", () => {
    const found: Found = { claude: yes, muse: no, codex: yes, pi: no, omp: yes, ollama };
    expect(pickable(found, AGENTS, true).map((o) => o.value)).toEqual(["claude", CLAUDE_OLLAMA, "codex", "omp"]);
    expect(pickable(found).map((o) => o.value)).toEqual(["claude", "codex", "omp"]); // helpers: never on Ollama
  });
  it("Claude Code (Ollama) needs Claude Code and the ollama CLI; without a model it shows why, disabled", () => {
    expect(pickable({ codex: yes, ollama }, AGENTS, true).map((o) => o.value)).toEqual(["codex"]);
    expect(pickable({ claude: yes, ollama: { version: "", error: "needs the ollama CLI" } }, AGENTS, true).map((o) => o.value)).toEqual(["claude"]);
    const noModel = pickable({ claude: yes, ollama: { version: "/x/ollama", error: "Ollama does not have glm: run ollama pull glm" } }, AGENTS, true);
    expect(noModel.find((o) => o.value === CLAUDE_OLLAMA)?.disabled).toContain("ollama pull glm");
    expect(choosePick(noModel, CLAUDE_OLLAMA, true)).toBe("claude"); // what is shown can start
  });
  it("never offers a hidden agent, even when installed", () => {
    expect(pickable({ claude: yes, copilot: yes, gemini: yes }).map((o) => o.value)).toEqual(["claude"]);
  });
  it("offers nothing until the install check answers", () => {
    expect(pickable(null, AGENTS, true)).toEqual([]);
  });
  it("a failed install check blocks nothing", () => {
    expect(pickable(checkFailed(), AGENTS, true).map((o) => o.value)).toEqual(["claude", CLAUDE_OLLAMA, "muse", "codex", "pi", "omp"]);
  });
  it("launches Claude Code (Ollama) as Claude via Ollama, on Ollama's model", () => {
    expect(launchAgent(CLAUDE_OLLAMA)).toEqual({ harness: "claude", via: "ollama" });
  });
  it("launches every agent on its own default model: no model is sent", () => {
    for (const a of ["claude", "codex", "muse", "pi", "omp", "shell"]) expect(launchAgent(a)).toEqual({ harness: a });
  });
  it("a child starts on its parent's agent, through Ollama when its parent was", () => {
    expect(parentPick({ harness: "claude", via: "ollama" })).toBe(CLAUDE_OLLAMA);
    expect(parentPick({ harness: "codex" })).toBe("codex");
    expect(parentPick({ harness: "shell" })).toBeUndefined();
  });
  it("the Ollama default beats a pick that is not offered", () => {
    const options = pickable({ claude: yes, codex: yes, ollama }, AGENTS, true);
    expect(choosePick(options, "", true)).toBe(CLAUDE_OLLAMA);
    expect(choosePick(options, "gone", true)).toBe(CLAUDE_OLLAMA);
    expect(choosePick(options, "codex", true)).toBe("codex");
    expect(choosePick(options, "", false)).toBe("claude");
    expect(choosePick(pickable({ codex: yes, ollama }, AGENTS, true), "", true)).toBe("codex"); // Claude Code is not installed
  });
  it("waits for both the install check and the settings", () => {
    expect(pickReady({ claude: yes }, false)).toBe(false);
    expect(pickReady(null, true)).toBe(false);
    expect(pickReady({ claude: yes }, true)).toBe(true);
  });
});
