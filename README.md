# pengupool-muse

Say a rule once; your coding agent stops asking, and you can measure that it did.
Built on Meta's Muse models for the Meta Global AI Developer Hackathon (Oct 27 – Nov 4, 2026).
See [DESIGN.md](DESIGN.md).

Python 3.9+, standard library only.

```bash
export MUSE_BASE_URL=...   # any OpenAI-compatible endpoint serving a Muse model
export MUSE_API_KEY=...
export MUSE_MODEL=...

python3 -m muse rule "from now on, you can run the test suite without asking"   # asks you to confirm
python3 -m muse list
python3 -m muse check '{"tool": "Bash", "input": {"command": "pytest -q"}}'
python3 -m muse forget <id>

python3 test_muse.py   # self-check, no network
```

The Compass lives in `~/.pengupool-muse/compass.jsonl` (`PENGUPOOL_MUSE_HOME` moves it).

## Status

- [x] Compass store, rule extraction, rule matching, CLI (Oct 26)
- [ ] Voice: Muse Voice Transcribe into `muse rule`
- [ ] Permission hook for the agent under test
- [ ] Gate: the same tasks with and without a change, interruptions / pass rate / seconds
