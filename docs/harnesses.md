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

Through those hooks each session gets your Compass, its team, and (for a helper) its journal,
and PenguPool sees its questions, permission prompts and turn ends. An agent PenguPool has no hooks
for is not offered. GitHub Copilot and Gemini CLI are hidden: their old sessions still run and resume.

## Who does what

| Job | Done by | Notes |
| --- | --- | --- |
| Coding | The agent you pick: Muse Code (Meta's coding agent), Claude Code, Codex, pi or OMP | Each agent chooses its own model; PenguPool passes a model only when you set one, and pins none |
| Triage: where a preference belongs, whether a check-in can go on, who does a task | Your decision model, one of three | Required. Local default: Together AI's Tev1, a Qwen fine-tune, through Ollama (not a Meta model); an online System One endpoint (Jev); or Muse Spark on the Meta Model API (Settings → Decision model → Meta · Muse Spark, then Use for triage). System One is calibrated. Muse Spark is not: with it, no answer acts on its own (check-ins go to you, the Compass asks, Experience keeps every report, team proposals keep each helper's own agent), and every call asks for structured JSON at reasoning effort `minimal`, fixed in code |
| Send-path triage: who does a task a lead or builder hands one of its members (`Task:` or `--type task`) | The same decision model | Any of the three. Its pick governs: a task it would give another running member of the sender's goes there (a calibrated model's only at p >= 0.80; a less sure pick leaves it as addressed, marked unsure), with its pick and its reasoning (Muse Spark) or probability (System One) on the message; the sender can keep its own pick with `--override "<reason>"`, which is recorded. A note that hands over no work is not checked. No usable answer within 10 s: the task goes as addressed, marked unavailable. No decision model: no check |
| Hand-off check: whether the lead should hand its next step to a member | The same decision model | One yes/no before each lead turn (not short answers, notices, or owner prompts that name a member or say "yourself"). A calibrated yes at p >= 0.80 holds the lead's working tools until it sends one of its members a task; a less sure yes, or any yes from Muse Spark, is a note only. No answer within 8 s: nothing |
| Answering from your phone | The Muse app, paired over Tailscale Serve | Supported team requests only; see [Phone access](phone.md) |

## Muse plugin

What it gives you: a Muse Code session that knows its team, your Compass and (for a helper) its own
journal, and that PenguPool can see well enough to put its questions and check-ins in front of you,
including on your phone.

How: PenguPool writes a Muse plugin manifest whose hooks call the `pengupool` CLI on session start and
end, each prompt, each tool use and permission request, and each turn end or failed turn
([`internal/harness/muse_plugin.go`](../internal/harness/muse_plugin.go)). The app installs and
approves the plugin in your Muse home. Your answers, from the app or a paired Muse device, return to
the session through PenguPool's owner-input and answer paths
([`internal/pool/muse.go`](../internal/pool/muse.go)). Not every hook has been tested on every
Muse version.

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
| Claude Code | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`), merged into `permissions.allow` | 10 rules |
| Codex | `~/.codex/rules/pengupool.rules` (or `$CODEX_HOME`), a file PenguPool owns | 10 rules |
| OMP | nothing: its default approval mode asks nothing | — |
| pi | nothing: it has no permission prompts | — |
| Muse Code | nothing yet: Muse has no documented rule format | — |

- The rules cover only `send`, `search`, `experience search`, `experience show`, `ask`, `answer`,
  `decide`, `team request add`, `team request remove` and `compass word`, named by the app's full
  path. The host still checks each call: an answer only from the owner's own prompt, a team change
  only as a request the owner approves, a Compass line only from the session PenguPool asked. Push,
  merge, release, delete and every other command still ask.
- Turned on before a command joined the list? It still asks until you turn the switch off and on;
  PenguPool never widens your yes on its own.
- On Claude Code 2.1.316 a command that chains one of these with anything else, redirects to a
  file, runs in the background or embeds another command still asks.
- Your own rules and keys stay as they were. Your first `settings.json` is kept as
  `settings.json.pengupool-backup`. A file PenguPool can't parse is left alone, with the reason shown.
- *Show what is written* lists every file and line. Turning it off removes exactly those lines. When
  the app moves, PenguPool rewrites its own lines at start.

## Terminal sessions

A plain Terminal session runs with no hooks. An agent you start by hand in one gets no Compass, no
team and no triage, and its `pengupool` calls carry your own terminal's trust. Terminal is not for
team members or unattended work.
