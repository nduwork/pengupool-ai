# PenguPool AI

PenguPool AI is the PenguPool agent-pool app — a Go host daemon with PTY worker
sessions, a browser/desktop terminal UI, helper teams and an owner Compass —
running Claude Code (account or Ollama), Muse Code, Codex, pi and OMP
side by side, with the Muse app as the owner's remote.

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
pengupool-ai daemon [--store DIR] [--http ADDR]   # state, sessions, browser app
pengupool-ai new [--name N] [--cwd DIR] [--harness claude|muse|codex|pi|omp]
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
  sandbox), pi and OMP, plus Claude Code (Ollama): Claude through
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

The Muse app reaches this machine over the tailnet through Tailscale Serve (Settings › Tailscale),
at `https://<machine>.<tailnet>.ts.net`. Settings › Muse app › "Pair a Muse device" shows the API URL and a
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
background menu. Copilot and Gemini CLI are experimental agents (OpenCode was, until v0.9.44);
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
v0.9.37: The host keeps each session's terminal history (2000 lines, tmux's default), and snapshots
carry it ahead of the screen (at most 512 KiB, newest lines first). The web app resets before every
snapshot, so it gets the history each time and every viewer shows the same one after a reconnect, a
resize or a session switch. `pengupool attach` writes snapshots into your terminal, so it gets the
history once, on attach. Memory: 112 bytes a cell, so about 27 MB per session at worst with full
120-column lines, 56 MB at 250 columns; typical output far less. Live output is unchanged, and so is
the phone and Muse API. Known limit: Claude Code repaints its live area in place (synchronized
output), and those repaints never become history lines, here or in any terminal
(stablyai/orca#18490).
v0.9.38: The owner can answer a child's waiting ask from the lead's session. The lead's context lists
its direct children's asks, and `pengupool ask answer ASK_ID [--option N]` carries the owner's answer.
The lead chooses no content: the answer is the owner's whole prompt, word for word (with anything
secret-looking left out; a prompt over 2000 characters is refused, never clipped), for the child to read. It is option N only when the whole prompt
is exactly N, "option N" or that option's text. It is accepted only when all of these hold:
- it comes from the lead's own process on this host;
- the lead's turn was opened by a prompt the owner typed;
- no teammate's message could have reached the lead since its previous reply: none pending, none
  delivered since, and no SendMessage to it let through since;
- the ask is a direct child's, made before that prompt, and the lead's previous reply showed it;
- that reply showed exactly one waiting question, or the owner's prompt names this one by its id.

A teammate's message, triage or a notice answers nothing. The answer is recorded as the owner's ("owner
via <session>") with the owner's words; harness permission prompts are not covered.
v0.9.39: The compass agents read holds the owner's prose lines only, guiding lines first, within 8 KiB
for the whole section. Permission answers ("The owner allows Bash for …") stay stored for answering
permission prompts but no longer fill agents' context, and every line is a candidate before any limit,
so a run of newer permission answers can no longer push the owner's preferences out. Each line shows
at most 360 characters, and a line that doesn't fit is skipped rather than ending the list, so one
long line can't hide the rest.
v0.9.40: A change of mind gets its own Compass line, and the Compass review shows lines that
contradict. When the owner's words only go against existing lines ("red is my favorite" after
"yellow"), the contradiction is recorded at once and the words are worded as a new line, so agents
still see the owner's current preference. With a decision model, the review lists preference lines
that contradict where their conditions overlap: a connected group is one item, each line shown with
its condition. The owner keeps one, and the lines that contradict it directly are deleted with their
evidence; a line that only contradicted another stays for the next review, and the kept line is
unchanged. A line edited since the review is refused ("refresh"). Without a decision model there are
no conflict items, and a review asks the model again only when a shown line changes.
v0.9.41: An ask the owner answers through the lead reaches the member as the owner's own prompt. When
the member is no longer waiting in `pengupool ask`, the owner's whole words are typed in as its next
prompt ("Answer to your ask ID: …"), credited to the owner, so the member's harness sees the owner's
prompt and not a teammate's relay; a waiting member still gets the answer from its wait, and a busy one
gets the words typed at its next stop, once. `pengupool ask "QUESTION" --option A --option B` keeps its options: flags after the question
were read as words of it.
v0.9.44: OpenCode support is removed: it is no longer offered, launched or resumed, and its plugin
is gone. A stored OpenCode session fails to resume with a clear error ("OpenCode is no longer
supported"); start a new session on another agent. The plugin file earlier launches wrote stays on
disk, unread.
v0.9.45: Removed: Local network (Settings, `--lan`, and the plain-HTTP pairing URL on the tailnet
name). Phones and other devices reach PenguPool only through Tailscale Serve; the host listens on
loopback alone and refuses any `--http` that is not a loopback address (`:PORT` means
`127.0.0.1:PORT`). A config.json with Local network on is ignored,
with a log line saying so.
v0.9.46: Sync with GitHub starts only once the owner picks its repository. Turning it on with none
picked syncs nothing and creates nothing; Settings says to choose a repository, suggesting
pengupool-memory, which is created only if the owner saves that name. A config that already names a
repository syncs as before.
v0.9.47: The owner's words reach the whole subtree. Any active member above another on the team's
tree (a closed member between them doesn't break the chain) sees the questions waiting on the owner
below it, asks and check-ins, at most 20 listed, and carries the owner's answer with `pengupool
answer ITEM_ID` (`ask answer` stays as an alias). The previous reply must show the item's id and
quote its question as it stands: an ask's first 200 characters, a check-in's last 200 (case,
spacing, `>` and backticks aside), so a paraphrase answers nothing. A check-in (`checkin:<id>`) is
answered by typing the owner's whole prompt into the member as the owner's own words, with the app's
check-in guards; the gate records the session it came through, and both run logs carry the full
text. `pengupool ask --owner-only` marks a question only the owner may answer, and helpers are told
to use it when one of their own rules stops them. Every #63 guard stays.

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
