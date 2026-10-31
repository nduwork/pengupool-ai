# Supported harnesses

PenguPool runs whatever agent you start in a terminal session; it does not install or manage agents.
Harnesses with PenguPool integration (hooks for context, Compass and the journal):

| Harness | Notes |
| --- | --- |
| Claude Code | Also as Claude Code (Ollama), see below |
| Muse Code | The app installs its PenguPool plugin |
| Codex | The app writes a rule so only the PenguPool CLI leaves the sandbox |
| pi, OMP | One PenguPool extension for both; found on the login PATH |

Only agents found installed are offered; an agent PenguPool has no hooks and messages for cannot
be added. GitHub Copilot and Gemini CLI are hidden: their old sessions still run and resume.

**Claude Code (Ollama)** is offered when Claude Code and the ollama CLI are both installed. It is a
full Claude Code session (hooks, Compass, teams, resume) started as
`ollama launch claude --model <M> --yes -- <Claude's own arguments>`. `<M>` is the model Ollama
would pick for Claude, read from Ollama's own record (`~/.ollama/config.json`: the model last used
with `ollama launch claude`, else Ollama's last model, else the first it lists), so no picker shows.
A model Ollama does not have is refused with the `ollama pull` to run; nothing is pulled for you.
Ollama's address is its own (`OLLAMA_HOST`, else the default); PenguPool keeps no model or address.
Source of truth: the agent list in `internal/harness`.

A hand-started agent in a Terminal session runs with no hooks (no guards, floor or triage) and its
`pengupool` calls carry the owner's terminal trust, as in any terminal. Terminal is not for team
members or unattended sessions.
