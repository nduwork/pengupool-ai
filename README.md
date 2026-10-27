# pengupool-muse

Say a rule once; your coding agent stops asking, and you can measure that it did.
Built on Meta's Muse models for the Meta Global AI Developer Hackathon (Oct 27 – Nov 4, 2026).
See [DESIGN.md](DESIGN.md). Each module lands as an `M<n>` step: self-check, a review by a Muse model, a working command, then a push.

Python 3.9+, standard library only.

```bash
# Meta Model API by default (key in META_API, model muse-spark-1.3); MUSE_* override for Baseten/Modal/Ollama

python3 -m pengu rule "from now on, you can run the test suite without asking"   # asks you to confirm
python3 -m pengu list
python3 -m pengu check '{"tool": "Bash", "input": {"command": "pytest -q"}}'
python3 -m pengu forget <id>
git diff HEAD | python3 -m pengu review   # Muse Code reviews the diff
python3 -m pengu new --agent muse --task "hello" --trust-workspace   # start a session in tmux
python3 -m pengu ls
python3 -m pengu stop <id>

python3 test_pengu.py   # self-check, no network

python3 -m pengu send --from <id> --to <id|owner> --text "..."   # chat along the tree
python3 -m pengu inbox <id>                                      # turns addressed to a session
python3 -m pengu ask --session <id> --question "..." --option A --option B  # wait for the owner
python3 -m pengu triage --text "..."   # route an owner-bound message
python3 -m pengu learned --helper H --title T --task ... --lesson ... --outcome worked
python3 -m pengu recall --helper H --task "..."   # <=5 lessons for this task
python3 -m pengu ingest --text "..." --session <id>   # propose a Compass change (M5)
python3 -m pengu confirm <proposal-id>   # apply it after your tap
```

Ingest (M5): `ingest` generalizes your words (Spark, off the hot path) and only
ever proposes; `confirm` applies. Evidence moves confidence (support up,
contradiction down, age discounts); `learned` generalizes a finished task into
lessons that merge with similar ones.

Inject (M6): lines are chosen by word overlap x confidence (0.3 floor) for
`instructions`, `compass_context` and the task text, so a session starts with
the relevant lines and not the others; `compass://lines` supports subscribe
and the server sends list_changed when the file moves mid-session.
Owner API (the Muse-app agent's window; stdlib HTTP on localhost):
`PENGUPOOL_OWNER_TOKEN` from the macOS keychain, then `python3 -m pengu owner-serve`.
Owner actions: `security add-generic-password -s pengu-owner -a owner -w <token>`;
launch with `PENGUPOOL_OWNER_TOKEN=$(security find-generic-password -s pengu-owner -a owner -w)`;
expose over the tailnet and set a Tailscale ACL limiting the port to device `muse`.
Agents must never run `pengu send --from owner`: a pty can fake a terminal.
Measured: `muse session-message` from outside a session is closed on this
machine, so chat is harness-side (inbox file + pane nudge).

The Compass host is an MCP server (`python3 -m pengu mcp`, stdio). Register it:
Claude Code: `claude mcp add -s user pengu-compass -e PENGUPOOL_MUSE_HOME=~/.pengupool-muse
-e PYTHONPATH=<repo> -- python3 -m pengu mcp`. Muse: a `pengu-compass` entry
under `mcpServers` in `~/.config/muse/settings.json` (same command/args/env).
Measured: Muse loads stdio tools, but server `instructions` do not reach its
prompt, so `pengu new --task` prepends the lines to the task instead.

The Compass lives in `~/.pengupool-muse/compass.jsonl` (`PENGUPOOL_MUSE_HOME` moves it).

## Status

- [x] M0 rename + `review`: a Muse Code session reviews diffs
- [x] M1 `session`: start/list/stop Claude and Muse sessions in tmux
- [x] M2 `mcp`: Compass MCP server both agents load (stdio; instructions verified per-client)
- [x] M3 `chat` + owner API: Claude <-> Muse messaging, questions release waiting sessions
- [x] M4 `judge` + experience: typed decisions, per-helper lessons, FTS5+judge retrieval
- [x] M5 `ingest` + `wording`: generalize, evidence/confidence, owner confirms
- [x] M6 `inject`: relevance x confidence selection; subscribe + list_changed
- [x] M7 `voice`: transcribe a clip, then ingest -> propose -> confirm
- [x] M8 `experiment`: with-vs-without on 5 tasks (chart below)

## Experiment (M8)

`python3 -m pengu experiment` runs five small tasks twice each: bare, and
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
task fixtures: `experiment/tasks.json`. Re-run: `python3 -m pengu
experiment --out <dir>` (needs `muse` on PATH and a local judge).
