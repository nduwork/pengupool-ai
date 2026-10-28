# PenguPool AI

PenguPool AI is the PenguPool agent-pool app — a Go host daemon with PTY worker
sessions, a browser/desktop terminal UI, helper teams and an owner Compass —
running Claude Code (account or Ollama), Muse Code, Codex and OMP/pi side by
side, with the Muse app as the owner's remote.

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
pengupool-ai new [--name N] [--cwd DIR] [--harness claude|muse|omp|pi|codex]
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
- **Harnesses.** Claude Code, Claude Code on Ollama, Muse Code (the app
  installs its PenguPool plugin), Codex (the app writes a rule so only the
  PenguPool CLI leaves the sandbox), OMP and pi. Only agents found installed
  are offered; Terminal runs any other.

Supported harnesses: docs/harnesses.md. See DESIGN.md for the dataflows.

## Verify

```bash
go test ./...        # full suite
bash scripts/smoke.sh        # host: daemon, PTY session, attach, worker adoption
make dist            # PenguPool-AI-<version>-mac-arm64.dmg
```

## Muse app

Settings › Phone connections walks the Tailscale setup (installed, logged in,
HTTPS, Serve, reachable), then "Pair a Muse device" shows the API URL and a
6-digit code for the Muse app. The Muse app then walks waiting sessions one
card at a time — Allow, Deny or Chat — over `/api/muse/v1` (contract in
DESIGN.md, spec at `/api/muse/v1/openapi.json`).

## Status

v0.2.0: PenguPool2 app base (host daemon/gateway/PTY workers, Electron shell,
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
v0.6.0: from PenguPool2 0.23.0, terminal scrollback kept across session
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
