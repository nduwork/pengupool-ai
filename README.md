# PenguPool AI

PenguPool AI is the PenguPool agent-pool app — a Go host daemon with PTY worker
sessions, a browser/desktop terminal UI, helper teams and an owner Compass —
running Claude Code (account or Ollama), Muse Code, Codex, pi, OMP and
OpenCode side by side, with the Muse app as the owner's remote.

Say a preference once; every agent starts with it, helpers remember their
finished work, and you answer waiting sessions from your phone.

## Install

Download `PenguPool-AI-<version>-mac-arm64.dmg` from this repository's
Releases, open it, drag `PenguPool AI.app` onto Applications, open it. The app
starts the host daemon for `~/.pengupool-ai/v2` and links `pengupool-ai` into
`~/.local/bin` on install.

From source: `make install` (Go 1.26.1 + Node 22 required).

## The host (PenguPool base)

```bash
pengupool-ai daemon [--store DIR] [--http ADDR] [--lan]   # state, sessions, browser app
pengupool-ai new [--name N] [--cwd DIR] [--harness claude|muse|codex|pi|omp|opencode]
pengupool-ai ls [--json]
pengupool-ai attach <session>
pengupool-ai kill <session>
pengupool-ai web        # browser app URL for the running daemon
pengupool-ai review [REV]          # Muse Code reviews a diff
pengupool-ai experiment            # with-vs-without the owner's lines
pengupool-ai version
```

Sessions are PTY workers that outlive the daemon; the desktop app and the
browser UI talk to the daemon over its gateway. Data: `~/.pengupool-ai/v2`
(`PENGUPOOL_HOME` moves it).

## Compass, journal, harnesses

- **Compass.** Your preferences, learned from your own words in any session
  (or the Muse app) and edited in the Compass panel; every session gets them
  through its harness hooks. Lines from the old `compass.jsonl` are imported
  once on first start.
- **Journal.** A helper's `Done:` report becomes an entry (its compacted
  context); its next session starts with the index and the matches.
- **Harnesses.** Claude Code, Muse Code (the app installs its PenguPool
  plugin), Codex (the app writes a rule so only the PenguPool CLI leaves the
  sandbox), pi, OMP and OpenCode, plus Claude Code (Ollama): Claude through
  `ollama launch claude` on Ollama's own default model. Only agents found
  installed are offered.

Supported harnesses: docs/harnesses.md. See DESIGN.md for the dataflows.

## Verify

```bash
go test ./...        # full suite
bash scripts/smoke.sh        # host: daemon, PTY session, attach, worker adoption
make dist            # PenguPool-AI-<version>-mac-arm64.dmg
```

## Muse app

With Local network on, the Muse app reaches this machine on its own Tailscale name over the
tailnet (`http://<machine>.<tailnet>.ts.net:7787`); Tailscale Serve (Settings › Tailscale) is the
HTTPS alternative. Settings › Muse app › "Pair a Muse device" shows the API URL and a
6-digit code for the Muse app. The Muse app then walks waiting sessions one
card at a time — Allow, Deny or Chat — over `/api/muse/v1` (contract in
DESIGN.md, spec at `/api/muse/v1/openapi.json`). Step by step, with curl examples and the owner
WebSocket API for agent clients: [tools/muse/muse-app-management.md](tools/muse/muse-app-management.md).

## Status

v0.2.0: app base (host daemon/gateway/PTY workers, Electron shell,
full web terminal UI) rebranded as PenguPool AI, with the owner-confirmed
Compass integrated into session context and shipped as the `pengu` CLI + MCP
server in the same DMG.
v0.3.0: Muse Code harness (interactive sessions in the app, MSP host
client with wire approvals, `muse exec` worker) alongside Claude Code
(account or Ollama-backed).
v0.4.0: harness-first. Muse gets hooks (context, Compass, journal), Codex,
OMP and pi promoted (Codex sandbox rule; OMP/pi binary path), Copilot,
Gemini and OpenCode removed; the separate Compass layer (`pengu` CLI, MCP,
owner API) folded into the built-in Compass with a one-time import; a
designated Muse app API with per-device pairing; Tailscale setup checklist.
v0.5.0: the Helper agent hub is `pengupool-agent-hub` (reads its
`.pengupool/catalog.json`); the agent picker lists only installed agents plus
a Terminal option for any other; Settings lists detected agents and drops the
OMP/pi binary-path fields.
v0.6.0: terminal scrollback kept across session
switches, flexible teams (no size cap, drop on a group, split a subtree) and
backup renamed to sync; agent sessions are resumed automatically after a
power off or crash.
v0.6.1: messages the owner queues while an agent is busy, joined by the
agent into one prompt, still count as the owner's.
v0.6.2: PenguPool adds no questions beyond the agent's own: a tool call the
host cannot check goes to the agent's permissions, and agents check the
Compass before asking the owner anything else.
v0.7.0: Compass lines read as the owner's rule or only likely; search
stems words; hedged words weigh half; lines pulled both ways go to review
(restate or remove); a Gaussian belief runs in shadow beside the Beta one,
reported by `pengupool eval` with a drift benchmark.
v0.7.1: a correction is read as a change of preference with PAHF's cue
phrases, and a narrower rule in it is kept as a new line; the drift
benchmark adds PAHF's shopping drift; PAHF credited.
v0.8.0: the Teams page folds into the map (group settings and close on each
group's frame, closed groups from the background menu); a helper's journal
entries carry a one-line lesson from its Done report, and the helper sees the
matching entries with every task, each once per session.
v0.8.1: the file preview draws Mermaid diagrams in Markdown, runs an HTML
page's scripts with its own assets (isolated), shows PDFs, and follows the
file as it changes; Muse sessions get their hooks again (the app replaces a
PenguPool plugin left from another build instead of failing on it).
v0.8.2: the app replaces a running host that is not its own build (one from an
older or ejected disk image could not start sessions); a Muse session's hook
answer fits Muse's 16 KiB limit, and Muse's read tools pass a triage hold. Two builds
sharing one store (a dev app and a release app) restart each other's host at
each launch; the app's log says so.
v0.8.3: a host that holds the store but no longer answers (run from a disk
image since ejected) is stopped, killed if it must be, and replaced.
v0.8.4: the Helper agent hub: the owner can set the hub's
repository in the Helper agents page; an install still on the old
pengupool-marketplace hub moves to pengupool-agent-hub once (helpers, keys and
journals follow); a bad catalog entry is skipped and named, not fatal.
v0.8.5: `pengupool doctor` checks the machine and only reads: PenguPool v1
leftovers, the agent CLIs, a leftover PenguPool MCP server, a Muse plugin that is
off or calls a missing binary or another store, and a host that is doubled, runs
from a disk image, or does not answer.
v0.9.0: updated host and app. Muse Code is an agent (plugin hooks, the hook answer
cap, Muse's read tools, the Muse app API and device pairing). No Teams page: group
settings and close are on the map's group frame, closed groups in the map's
background menu. Copilot, Gemini CLI and OpenCode are experimental agents;
`pengupool doctor` can clean PenguPool v1 out of Claude's settings. A record
written by 0.4–0.8 opens: its search indexes are remade and the experience lessons
column is not kept; update every machine that shares the record together.
v0.9.1: a session opened by hand in a helper's folder works as that helper again
when the helper lives in a subfolder of its repository, so `experience search`
reaches its journal. Update checks look at this repository's releases (they
answered 404). A remote host whose PenguPool sits in the app, in a folder with a
space, is reached again.
v0.9.2: the app again restarts a host that is not its own build (and one that holds
the store but does not answer), so after an update the host runs the new version;
0.9.0 had lost this. `make install` replaces the app in /Applications when it is
there.
v0.9.3: tests hold the host to the app's build (the app restarts an older host;
the daemon writes the version line the app compares). Saving settings no longer
races a Muse device being revoked. `pengupool new` lists muse again.
v0.9.4: the file preview shows Mermaid diagrams, PDFs and HTML that runs its own
scripts again, and follows a file as an agent writes it. The Tailscale setup is back:
the checklist in Phone connections, the Tailscale app's CLI found when it is not on
PATH, the approval link when Serve needs one, and Serve re-pointed after the host's
port changes.
v0.9.5: after a reboot or crash ended running sessions, the app asks whether to
restore them (agents resume their conversations). Sessions keep running through an
app quit and an update, and are not asked about.
v0.9.6: the restore question after a reboot also offers Start fresh: the stopped sessions
leave the map and a team with none of its members running is closed (under Closed groups).
Nothing is deleted on disk; scrollback, worktrees, team notes and journals stay.
v0.9.7: team roles LEAD, SME and HELPER replace the anchor (old anchor data is ignored).
Settings is grouped by topic (General, Agents, Decision model, GitHub, Local network,
Tailscale, Muse app, SSH hosts). New session offers installed agents only, plus Claude Code
(Ollama): Claude through `ollama launch claude` on Ollama's own default model (a missing model
is refused with the `ollama pull` to run). A session waiting on you reads "? Need input" with
a red outline. Muse sessions become ready when their prompt shows, so team messages reach them.
Removed: Set anchor; the Claude Code on Ollama card (address, model, "every session on
Ollama", which moves to New session's default); per-session Ollama models; Ollama for helpers,
team members and the other agents; GitHub Copilot and Gemini CLI from the pickers (their old
sessions still resume); the "(experimental)" labels.
v0.9.8: New session and Add member start every agent on its own default model.
Removed: the Model field in New session and in Add member (`pengupool new --model` still
works, and a session resumes on its recorded model).
v0.9.25: New session ▸ New worktree is back: an agent starts in a new worktree of a local
repository, kept in PenguPool's own folder, on a new branch (`pengupool/<name>` by default) or
an existing one.
v0.9.28: The Muse app reaches PenguPool on this machine's own Tailscale name, without Serve
(Local network on); the pairing URL names it. With Local network on, the desktop app no longer
sits at Reconnecting. Pairing a name already paired, or with no name, says so (409, 400) instead
of 502. New guide: tools/muse/muse-app-management.md.
v0.9.29: A session waiting on you shows what it asks (the permission, the question and its
choices, or the plan to approve) on hover in the app, in `session.list` and in the Muse app's
session list. It is still answered in the session's terminal.
v0.9.30: An agent can put a question only you can settle to you directly (`pengupool ask
[--option A]... "question"`). It waits for your answer, given in the app's Approvals or from a
paired Muse device: one of its options, or a reply in your own words. Other agents cannot answer it. An ask left
unanswered for 15 minutes expires, and the agent goes on with a safe default of its own.
v0.9.31: A paired Muse device manages your agent teams: `/sessions` shows each session's team,
role, parent and assignment; a member's ask the team left to you waits on `/next` (go on, reply,
dismiss); `POST /sessions/{id}/action` stops, interrupts or resumes an agent session, with no
error when it already is so; `POST /teams/{id}/members` asks for a team change that waits for
your allow. Teams without iOS access stay hidden, and no agent can use any of it.
v0.9.32: The PenguPool app stays on this machine. Over the network (Local network, the tailnet
name, Tailscale Serve) only the paired-device API and the health check answer; the app, its
WebSocket and every other route are 404 there, reachable on this machine or through an SSH
port forward. Removed: the browser phone view (the app opened without the token from a phone or
another machine on the network), and with it the team's group password, which only that view
asked for. Phones use a paired Muse device. A raw TCP forward you set up to the port (SSH,
socat, Serve in TCP mode) counts as this machine, as an SSH forward does; PenguPool's own Serve is
the HTTPS kind.
v0.9.33: The Muse companion (change detector, status card, answer cards, mini status site) ships
at tools/muse/companion. It uses only the paired-device API with a device token; it never holds the
owner's app token. The mini status site on this machine is a paired device of its own, and its URL
carries a key without which every page is 404. `/sessions` also gives each running agent's status
(working, idle...).
v0.9.34: Removed: the team group password backend, unused since v0.9.32 took away the browser
phone view: the team.lock and team.unlock calls, the phone unlock check and the stored password
verifiers. A state.json from an older version still loads; its stored verifiers are ignored, with
no error, and gone from state.json at the next save and from its backup, state.json.bak, at the
save after (backups you made yourself keep them). A team's iOS access still decides whether paired devices see
it.
v0.9.35: Team members know each other by session id, not display name, so renaming a session no
longer breaks who reaches whom. The Team members list maps each session id to its current name,
and the send and reply commands PenguPool hands agents name the session id. Agents still call
teammates by display name when they talk to the owner. Sending by name still works.
v0.9.36: A Claude SendMessage is checked against the session Claude actually delivers it to, the
Claude session of that name. A name shared with another team's session no longer slips past the
team check, and a name two Claude sessions share is refused with a pointer to `pengupool send
--to <session id>`.
v0.9.37: The host keeps each session's terminal history (2000 lines, tmux's default), and an
attach snapshot carries it ahead of the screen (at most 512 KiB, newest lines first), so every
viewer shows the same history after a reconnect, a resize or a session switch. Live output is
unchanged, and so is the phone and Muse API. Known limit: Claude Code repaints its live area in
place (synchronized output), and those repaints never become history lines, here or in any
terminal (stablyai/orca#18490).

## Credits

- **PAHF** — *Learning Personalized Agents from Human Feedback*, Kaiqu Liang, Julia Kruk,
  Shengyi Qian, et al. (Meta Superintelligence Labs, Princeton University, Duke University),
  [github.com/facebookresearch/PAHF](https://github.com/facebookresearch/PAHF), MIT License.
  The Compass's memory pipeline was compared against PAHF's: its feedback loop (keep only
  personal information, summarise it in one sentence, update after a correction) and its
  finding that stale preferences used confidently hurt most after a change shaped how the
  Compass weighs corrections and fades silent lines. Borrowed: the change-of-preference cue
  phrases and the carve-out for narrower rules in the question that decides whether a
  correction changes a preference (from PAHF's `check_update_prompt`), and the drift rate of its
  shopping personas (682 of 1200 preference lines change) as a scenario in the Compass drift
  benchmark. No PAHF code or data files are included. PAHF is Copyright (c) Meta Platforms,
  Inc. and affiliates, under the MIT License.
