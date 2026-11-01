# Troubleshooting

## An agent I installed isn't offered

PenguPool looks for agents on your login shell's `PATH`. Check the agent runs in a new Terminal window
(`claude --version`, `codex --version`, …), then Settings → Agents → **Check again**. Claude Code
(Ollama) also needs the `ollama` CLI. `pengupool doctor` lists the agent CLIs it finds.

## The decision model check fails

- **Local (Ollama):** is Ollama running (`ollama serve`), and is the model pulled
  (`ollama pull <model>`)? System One needs Ollama 0.35 or later. PenguPool never installs Ollama or
  pulls weights.
- **"Not in the decision format":** pick a System One decision model. General chat models aren't
  supported for triage.
- **Online:** check the address and the API key.

After a change, **Save provider** and run **Check connection** again; a model is used for triage only
after its check passes.

## Agents keep asking permission for PenguPool's commands

Allow them in Settings → Agents → **Agent permissions**. If a row reads **not written**, its reason
says why (for example a `settings.json` PenguPool can't parse; fix it and turn the setting off and on).
**changed** means some of PenguPool's lines were removed outside PenguPool; turn it off and on to write
them again. A session that was already running may need a restart to pick the rules up.

## The app can't reach its host, or sessions look stuck

Settings → General → **Restart host**. Sessions keep running and are picked up again. If the app
can't start the host at all, quit and reopen the app.

## My phone can't connect

Work through Settings → **Tailscale**; every step must pass. The phone must be on the same tailnet. A
pairing code works once, for 5 minutes, and ten wrong codes pause pairing for a minute. A revoked
device has to pair again.

## Reporting a bug

Open an [issue](https://github.com/nduwork/pengupool-ai/issues/new/choose) with the app version
(Settings → General), your macOS version, the agent and its version, what you did, and what happened
instead. Leave out API keys, tokens, customer data and private paths; trim logs to the relevant lines.
The host's logs are in `~/.pengupool-ai/v2/logs`.
