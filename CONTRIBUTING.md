# Contributing

Thanks for helping. Bugs and feature ideas go in
[Issues](https://github.com/nduwork/pengupool-ai/issues/new/choose); changes come as pull requests.

## Build from source

You need macOS on Apple Silicon, Go 1.26.1 and Node 22.

```bash
make install    # build PenguPool AI.app (Electron, with the Go host inside), install it, link the CLI
make app        # build the app into desktop/dist/mac-arm64 without installing it
make dist       # the disk image, build/PenguPool-AI-<version>-mac-arm64.dmg
make web        # the browser app, into internal/webapp/dist (embedded in the host)
make uninstall  # remove the app and the CLI link; your data in ~/.pengupool-ai stays
```

## Test

```bash
go test ./...                 # the Go suite (make test adds -race)
cd web && npx vitest run      # the web app's unit tests
bash scripts/smoke.sh         # host smoke test: daemon, PTY session, attach, worker adoption
```

Tests never touch your real home: anything that writes an agent's settings runs on a temporary
directory.

## Releasing

1. Bump `Version` in `cmd/pengupool/main.go`, add the release's entry to `CHANGELOG.md`, and merge to main.
2. Publish release `v<version>` on main: `gh release create v<version> --target main --notes "…"`, or in the
   web UI. A person (or a personal access token) must publish it: a release made with a workflow's own
   GITHUB_TOKEN starts no workflow.
3. The **release** workflow then builds, signs and notarizes the disk image on the self-hosted Mac, with that
   Mac's keychain, and attaches it to the release in about 15 minutes. It builds only a tag that is on main
   and matches `Version`. To retry a failed build, re-run the workflow: the image is replaced.

Releases go through CI. `make release` on a maintainer's Mac is the manual fallback; the release it publishes
starts the workflow too, which rebuilds and replaces the image. Pull requests and pushes are tested on
GitHub-hosted runners (`test.yml`); only the release workflow runs on the self-hosted Mac.

## How it fits together

```
 PenguPool AI.app (Electron, desktop/)        browser (web/, built into internal/webapp/dist)
          │  loads the web app, starts the host        │
          └────────────── gateway (internal/gateway) ──┘   unix socket for the CLI, loopback HTTP/WebSocket
                                   │
                            pool (internal/pool): teams, sessions, helpers, Compass, triage
                       ┌───────────┼───────────────┬───────────────────┐
             hub (internal/hub)  store (JSON)   agentdb (SQLite)   decision (System One)
                  │               state/config   Compass, journals   triage questions
         workers (internal/session): one PTY per session, outliving the host
                  │
         agents launched with PenguPool hooks (internal/harness)
```

| Package | Role |
| --- | --- |
| `cmd/pengupool` | The CLI and the host daemon entry point |
| `internal/pool` | PenguPool's domain: repositories, sessions, teams, helpers, the Compass, triage, the Muse app API |
| `internal/harness` | How each agent is launched, resumed and hooked; the agent permission rules |
| `internal/hub`, `internal/session`, `internal/vt` | Workers: one PTY per session, its terminal state, adoption after a host restart |
| `internal/gateway` | The daemon's front door: unix socket, HTTP and WebSocket, the Muse device API |
| `internal/store` | Durable state as JSON under `$PENGUPOOL_HOME/v2` |
| `internal/agentdb` | The record: Compass lines, helpers' journals, gates (SQLite) |
| `internal/decision` | The System One triage client |
| `internal/node` | SSH hosts the app can switch to |
| `internal/pool/skilltemplate` | Templates shipped inside helpers; changing them changes the product |
| `web/` | The React app (Vite, Tailwind, Radix) |
| `desktop/` | The Electron shell |
| `tools/muse/` | The Muse app guide and the Muse companion |

## Pull requests

- Keep a change small and about one thing; describe what it changes and how you checked it.
- Add or update a test for logic you change. Run the Go and web suites before you push.
- If you change the web app, rebuild `internal/webapp/dist` (`make web`) and commit it; the host
  embeds it.
- A user-facing change bumps the version in `cmd/pengupool/main.go`, `desktop/package.json`,
  `desktop/package-lock.json`, `web/package.json` and `web/package-lock.json`, and adds an entry at the top
  of [CHANGELOG.md](CHANGELOG.md).
- Keep the PR description in step with the code when you push more commits.
- Never include API keys, tokens, customer data or private paths in code, tests, logs or screenshots.

## Credits

PenguPool's Compass draws on PAHF (Meta Superintelligence Labs, Princeton University, Duke University);
the full attribution is in the [README](README.md#credits).
