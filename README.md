# PenguPool AI

Say a rule once; your coding agent stops asking, and you can measure that it did.

See [DESIGN.md](DESIGN.md). The Compass host is Go (`cmd/pengu` + `internal/*`); the
desktop app is Electron (`desktop/`); the renderer is React (`web/`, embedded via
`internal/webapp`).

Go 1.26.1 required to build; Node 22 to build the app.

## Install

Download `PenguPool-AI-<version>-mac-arm64.dmg` from this repository's
Releases, open it, drag `PenguPool AI.app` onto Applications, open the app.
The app starts its owner API on localhost and links the `pengu` CLI into
`~/.local/bin` (must be on your `PATH`).

From source: `make install` (Go + Node required).

```bash
pengu rule "from now on, you can run the test suite without asking"   # asks you to confirm
pengu list
pengu check '{"tool": "Bash", "input": {"command": "pytest -q"}}'
pengu forget <id>
git diff HEAD | pengu review   # Muse Code reviews the diff
pengu new --agent muse --task "hello" --trust-workspace   # start a session in tmux
pengu ls
pengu stop <id>

go test ./...   # self-check, no network

pengu send --from <id> --to <id|owner> --text "..."   # chat along the tree
pengu inbox <id>                                      # turns addressed to a session
pengu ask --session <id> --question "..." --option A --option B  # wait for the owner
pengu triage --text "..."   # route an owner-bound message
pengu learned --helper H --title T --task ... --lesson ... --outcome worked
pengu recall --helper H --task "..."   # <=5 lessons for this task
pengu ingest --text "..." --session <id>   # propose a Compass change (M5)
pengu confirm <proposal-id>   # apply it after your tap
```

Ingest (M5): `ingest` generalizes your words (Spark, off the hot path) and only
ever proposes; `confirm` applies. Evidence moves confidence (support up,
contradiction down, age discounts); `learned` generalizes a finished task into
lessons that merge with similar ones.

Inject (M6): lines are chosen by word overlap x confidence (0.3 floor) for
`instructions`, `compass_context` and the task text, so a session starts with
the relevant lines and not the others; `compass://lines` supports subscribe
and the server sends list_changed when the file moves mid-session.

Owner API (the Muse-app agent's window; loopback HTTP on port 8471 by default;
`PENGUPOOL_OWNER_TOKEN` from the macOS keychain, or the store's `owner-token`
file the desktop app writes):
`PENGUPOOL_OWNER_TOKEN=$(security find-generic-password -s pengu-owner -a owner -w) pengu owner-serve`.
Expose over the tailnet and set a Tailscale ACL limiting the port to device `muse`.
Agents must never run `pengu send --from owner`: a pty can fake a terminal.
Measured: `muse session-message` from outside a session is closed on this
machine, so chat is harness-side (inbox file + pane nudge).

The Compass host is an MCP server (`pengu mcp`, stdio). Register it:
Claude Code: `claude mcp add -s user pengu-compass -- pengu mcp`. Muse: a `pengu-compass` entry
under `mcpServers` in `~/.config/muse/settings.json` (same command).

The Compass lives in `~/.pengupool-ai/compass.jsonl` (`PENGUPOOL_AI_HOME` moves it;
a legacy `~/.pengupool-muse` file migrates once).

## Status

v0.1.0: full Go port of the prototype (compass, ingest, inject, judge, experience,
session, chat, mcp, owner, voice, review, experiment), Electron shell with a
renderer for lines, proposals, sessions, inbox and the experiment chart,
DMG + GitHub release.

## Experiment (M8)

`pengu experiment` runs five small tasks twice each: bare, and
with the task's Compass lines prepended (the same header `pengu new`
prepends; no MCP server registered, so the prompt text is the only
difference). Per run: the owner's decision matched or not (local judge
rates the transcript against the expected decision), questions asked
(interruption proxy), and the task's own pass check. Oct 26 run, judge
`qwen3.8:27b` on Ollama:

| task | arm | decision | interruptions | pass |
|------|-----|----------|----------------|------|
| run-tests | without | matched | 0 | pass |
| run-tests | with | matched | 0 | pass |
| migrations | without | missed | 0 | pass |
| migrations | with | matched | 0 | pass |
| quiet-tests | without | missed | 0 | pass |
| quiet-tests | with | matched | 0 | pass |
| changelog | without | missed | 0 | FAIL |
| changelog | with | matched | 0 | pass |
| small-commits | without | missed | 0 | FAIL |
| small-commits | with | matched | 0 | pass |

With the lines: 5/5 decisions matched, 5/5 passed. Without: 1/5, 2/5.
Interruptions read 0 in both arms: headless runs never ask, so the proxy
only bites in interactive sessions. Full rows: `experiment/results.json`;
task fixtures: `experiment/tasks.json`.
