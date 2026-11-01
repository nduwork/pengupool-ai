# Agents (harnesses)

PenguPool runs the coding agents installed on your Mac. It does not install them or sign you in:
install each one and sign in to it the way its maker says. New session offers only the agents it
finds installed. Source of truth: the agent list in `internal/harness`.

| Agent | How PenguPool connects to it |
| --- | --- |
| **Claude Code** | Hooks and a status line passed in each session's own launch settings |
| **Claude Code (Ollama)** | Claude Code started through `ollama launch claude`, with the same hooks (below) |
| **Codex** | Hooks passed per launch with `-c hooks.*` |
| **Muse Code** | A PenguPool plugin that the app installs and approves for you |
| **pi** and **OMP** | One PenguPool extension shared by both |

Through those hooks each session gets your Compass, its team, and (for a specialist) its journal,
and PenguPool sees its questions, permission prompts and turn ends. An agent PenguPool has no hooks
for is not offered. GitHub Copilot and Gemini CLI are hidden: their old sessions still run and resume.

## Who does what

| Job | Done by | Notes |
| --- | --- | --- |
| Coding | The agent you pick: Muse Code (Meta's coding agent), Claude Code, Codex, pi or OMP | Each agent chooses its own model; PenguPool passes a model only when you set one, and pins none |
| Triage: where a preference belongs, whether a check-in can go on | Your System One decision model | Required. Local default: Together AI's Tev1, a Qwen fine-tune, through Ollama (not a Meta model); or an online System One endpoint |
| Answering from your phone | The Muse app, paired over Tailscale Serve | Supported team requests only; see [Phone access](phone.md) |

## Claude Code (Ollama)

Offered when Claude Code and the `ollama` CLI are both installed. It is a full Claude Code session
(hooks, Compass, teams, resume) started as
`ollama launch claude --model <M> --yes -- <Claude's own arguments>`. `<M>` is the model Ollama would
pick for Claude, read from Ollama's own record (the model last used with `ollama launch claude`, else
Ollama's last model, else the first it lists). A model Ollama doesn't have is refused with the
`ollama pull` to run; nothing is pulled for you. Ollama's address is its own (`OLLAMA_HOST`, else the
default).

## Agent permissions

Agents in a team use PenguPool's command-line tool to message each other and look up past work, and
each call can stop on the agent's own permission prompt. Settings → Agents → **Agent permissions** is
off until you allow it. When on, PenguPool writes rules into each agent's own global settings, so they
also apply to sessions you start outside PenguPool:

| Agent | Where | What |
| --- | --- | --- |
| Claude Code | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`), merged into `permissions.allow` | 4 rules |
| Codex | `~/.codex/rules/pengupool.rules` (or `$CODEX_HOME`), a file PenguPool owns | 4 rules |
| OMP | nothing: its default approval mode asks nothing | — |
| pi | nothing: it has no permission prompts | — |
| Muse Code | nothing yet: Muse has no documented rule format | — |

- The rules cover only `send`, `search`, `experience search` and `experience show`, named by the app's
  full path. Push, merge, release, delete and every other command still ask.
- On Claude Code 2.1.316 a command that chains one of the four with anything else, redirects to a
  file, runs in the background or embeds another command still asks.
- Your own rules and keys stay as they were. Your first `settings.json` is kept as
  `settings.json.pengupool-backup`. A file PenguPool can't parse is left alone, with the reason shown.
- *Show what is written* lists every file and line. Turning it off removes exactly those lines. When
  the app moves, PenguPool rewrites its own lines at start.

## Terminal sessions

A plain Terminal session runs with no hooks. An agent you start by hand in one gets no Compass, no
team and no triage, and its `pengupool` calls carry your own terminal's trust. Terminal is not for
team members or unattended work.
