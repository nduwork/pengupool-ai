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

- [x] Compass store, rule extraction, rule matching, CLI (Oct 26)
- [ ] Voice: Muse Voice Transcribe into `pengu rule`
- [ ] Permission hook for the agent under test
- [ ] Gate: the same tasks with and without a change, interruptions / pass rate / seconds
