# Supported harnesses

PenguPool runs whatever agent you start in a terminal session; it does not install or manage agents.
Harnesses with PenguPool integration (hooks for context, Compass and the journal):

| Harness | Notes |
| --- | --- |
| Claude Code | Also on a local Ollama model |
| Muse Code | The app installs its PenguPool plugin |
| Codex | The app writes a rule so only the PenguPool CLI leaves the sandbox |
| OMP, pi | Found on the login PATH |

Any other agent: pick Terminal in New session for a plain shell in the folder, without the hooks.
Only agents found installed are offered.
Source of truth: the agent list in `internal/harness`.

A hand-started agent in a Terminal session runs with no hooks (no guards, floor or triage) and its
`pengupool` calls carry the owner's terminal trust, as in any terminal. Terminal is not for team
members or unattended sessions.
