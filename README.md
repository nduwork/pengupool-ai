# PenguPool AI

PenguPool AI is the PenguPool agent-pool app — a Go host daemon with PTY worker
sessions, a browser/desktop terminal UI, helper teams and an owner Compass —
plus the PenguPool AI improvement: an owner-confirmed Compass (the `pengu`
CLI + MCP server) whose lines ride along in every session's context.

Say a rule once; your coding agent stops asking, and you can measure that it did.

## Install

Download `PenguPool-AI-<version>-mac-arm64.dmg` from this repository's
Releases, open it, drag `PenguPool AI.app` onto Applications, open it. The app
starts the host daemon for `~/.pengupool-ai/v2` and links `pengupool-ai` (host)
and `pengu` (Compass) into `~/.local/bin` on install.

From source: `make install` (Go 1.26.1 + Node 22 required).

## The host (PenguPool base)

```bash
pengupool-ai daemon [--store DIR] [--http ADDR] [--lan]   # state, sessions, browser app
pengupool-ai new [--name N] [--cwd DIR] [--harness claude|muse|omp|pi|codex|copilot]
pengupool-ai ls [--json]
pengupool-ai attach <session>
pengupool-ai kill <session>
pengupool-ai web        # browser app URL for the running daemon
pengupool-ai version
```

Sessions are PTY workers that outlive the daemon; the desktop app and the
browser UI talk to the daemon over its gateway. Data: `~/.pengupool-ai/v2`
(`PENGUPOOL_HOME` moves it).

## The Compass improvement (`pengu`)

The Compass is one JSON-lines file of rules only the owner confirms
(`~/.pengupool-ai/compass.jsonl`). Evidence moves confidence (support up,
contradiction down, age discounts); agents never write there. Confirmed lines
are prepended to every host session's context next to the pool's own compass,
and are served to any harness over MCP.

```bash
pengu rule "from now on, you can run the test suite without asking"  # you confirm
pengu list
pengu check '{"tool": "Bash", "input": {"command": "pytest -q"}}'
pengu forget <id>
pengu mcp   # Compass MCP server on stdio (Claude Code + Muse Code load it)
pengu ingest --text "..."   # propose a Compass change (Spark generalizes)
pengu confirm <proposal-id> # apply it after your tap
pengu learned --helper H --title T --task ... --lesson ... --outcome worked
pengu recall --helper H --task "..."   # <=5 lessons for this task
pengu experiment            # with-vs-without on 5 tasks (decisions/interruptions/pass)
pengu muse-run "task"       # drive Muse over MSP; approvals on the wire (--yes approves)
pengu muse-exec "prompt"    # one headless Muse run (muse exec --json)
```

Muse Code is a first-class harness: pick it in New Session for the
interactive TUI, or drive it headlessly over MSP (`internal/musehost`:
initialize/session/turn with approval chosen on the wire). `muse-run`
registers the pengu MCP server into the Muse session natively.

Register the MCP server: Claude Code: `claude mcp add -s user pengu-compass -- pengu mcp`;
Muse: a `pengu-compass` entry under `mcpServers` in `~/.config/muse/settings.json`.

## Verify

```bash
go test ./...        # full suite
bash scripts/smoke.sh        # host: daemon, PTY session, attach, worker adoption
sh scripts/smoke-pengu.sh    # compass: CLI, owner API contract, MCP stdio
make dist            # PenguPool-AI-<version>-mac-arm64.dmg
```

## Status

v0.2.0: PenguPool2 app base (host daemon/gateway/PTY workers, Electron shell,
full web terminal UI) rebranded as PenguPool AI, with the owner-confirmed
Compass integrated into session context and shipped as the `pengu` CLI + MCP
server in the same DMG.
v0.3.0: Muse Code harness (interactive sessions in the app, MSP host
client with wire approvals, `muse exec` worker) alongside Claude Code
(account or Ollama-backed).
