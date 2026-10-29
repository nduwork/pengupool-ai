# Supported harnesses

PenguPool runs whatever agent you start in a terminal session; it does not install or manage agents.
Harnesses with PenguPool integration (hooks for context, Compass and the journal):

| Harness | Notes |
| --- | --- |
| Claude Code | |
| Muse Code | The app installs its PenguPool plugin |
| Codex | The app writes a rule so only the PenguPool CLI leaves the sandbox |
| pi, OMP | One PenguPool extension for both; found on the login PATH |
| OpenCode | A PenguPool plugin, given inline |

Only agents found installed are offered. Anything else is a Custom agent (Settings › Agents): a
name and the command that starts it. One that starts an agent above (for example
`ollama launch claude --model qwen3-coder --yes --`, which runs Claude Code on an Ollama model) ends
with `--`, and PenguPool adds that agent's own arguments and hooks after it. One that starts nothing
runs as written: no hooks, no teams, no Compass, no messages. GitHub Copilot and Gemini CLI are
hidden: their old sessions still run and resume.
Source of truth: the agent list in `internal/harness`.

A hand-started agent in a Terminal session runs with no hooks (no guards, floor or triage) and its
`pengupool` calls carry the owner's terminal trust, as in any terminal. Terminal is not for team
members or unattended sessions.
