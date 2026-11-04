# muse-companion — a companion executive for Muse + PenguPool

The small toolset that lets a Muse (in the Muse app) keep an eye on a
PenguPool team running on the owner's workstation, from the owner's
phone: a status monitor that stays silent unless something material
changes, a CLI that treats the check timer as configuration (never a
code change), and a status card rendered for chat with PenguPool's own
tree and status indicators.

This is the reference implementation behind the repo's
`tools/muse/muse-app-management.md` tutorial.

**Access model (v3):** the companion talks to PenguPool through the
paired device API (`/api/muse/v1`) only. The gateway's `/ws` owner
surface is local-only — the host guard answers 404 to non-loopback
requests for everything except `/api/healthz` and the device API — and
the companion machine holds only its paired device token. The daemon
bootstrap token never leaves the workstation.

**Installing with an agent:** point a Muse at this repo and ask it to
set up the companion — [`SETUP.md`](SETUP.md) is the step-by-step it
follows, including the Tailscale setup (the free Personal plan is
enough), the one-time steps only the owner can do, and the checks that
prove the install works.

## Pieces

| File | Role |
| --- | --- |
| `src/pp.py` | Device-API client: the Tailscale tunnel plus helpers for `/api/muse/v1` (sessions, next, say, answers, healthz). Device token from `$PENGUPOOL_DEVICE_TOKEN` or `./device-token` (0600); host from required `$PENGUPOOL_HOST` (name or IP), `$PENGUPOOL_HOST_HEADER` defaulting from it, `$PENGUPOOL_TRANSPORT` serve/serve-http/direct. The retired `/ws` client used to live here. |
| `src/detect.py` | The change detector. One poll prints one JSON line: `{"decision": "silent"|"wake", ...}`. Wakes only on material change: a session appearing/disappearing, a status change, a device-visible ask (`/next`), a daemon restart (the `/api/healthz` epoch changes), reachability flips. |
| `src/companion.py` | The CLI (below). Owns the monitor config. |
| `src/gadget.py` | Renders the team status card (chat-widget HTML) from live API data. |
| `src/tracker.py` | Per-agent workflow chains (A → B → C with a status symbol per step) rendered under each agent's name in the status card, from Muse's own `tracker.json` (tracking lives outside PenguPool by the owner's rule). |
| `src/askcard.py` | Renders the context card for a pending agent question (and its closed state). See "Answering agent questions". |
| `src/collect_status.py` | One-shot status collector: prints one compact JSON document (the team from `/sessions`, the pending ask from `/next`) for a live-status page. |
| `src/serve.mjs` | The optional mobile mini site (zero-dependency Node; runs on the PenguPool machine). |
| `install-host.sh` | Installs the mini site on the PenguPool machine (launchd). |
| `tracker.example.json` | Seed for the work tracker, copied on first install. |
| `hook/pengupool-status.sh` | The every-minute hook wrapper: runs the detector and forwards its decision. |
| `install.sh` | Installs the layout below; idempotent. |
| `config.example.json` | Default monitor config, seeded on first install. |
| `SETUP.md` | The full setup guide — Tailscale, pairing, tokens, verification — written for an agent to execute semi-automatically. |

## How the timer works (no code changes)

The hook polls the detector every 60 seconds. The detector reads
`~/workspace/companion/config.json` on **every** poll and only performs
a check when the configured interval has elapsed:

- `enabled: false` → always silent.
- `now − last_check < interval_secs` → silent, "not due", no API call.
- `companion check` forces one pass regardless.

So the check cadence is whatever the owner says — 1m, 5m, 1h — set with
one config write. 60s is the floor set by the base poll.

## CLI

```
companion status                  config + last observed team state
companion config                  show the monitor config
companion config set interval 5m  set the check timer (90 | 90s | 5m | 1h)
companion config set enabled off  pause / on resume
companion check                   run one check right now
companion log [n]                 the last n poll decisions
companion gadget [--out PATH]     render the team status card (HTML)
companion tracker                 print the agent workflow chains (text)
companion ask SPEC [--answer T]   render a question's context card (HTML)
```

## The status card

`gadget.py` rebuilds, from live data, the card Muse shows in chat:
the team as a tap-to-fold tree (the real parent links from the team's
member list; folding any parent row hides its branch), each agent's
workflow chain under its name, and PenguPool's **own** status
indicators — labels
and colours mirror `web/src/app/ctx.tsx` (`STATUS`, `shownStatus`) and
the design tokens in `web/src/index.css`:

| Status | Label | Colour |
| --- | --- | --- |
| working | Active | green, pulsing (`#15803d` / `#4ade80`) |
| idle | Waiting | amber (`#b45309` / `#fbbf24`) |
| approval | ? Need input | red (`#c0262d` / `#f87171`) |
| stale, ended, exited | Stale / Ended / Exited | grey (`#8a91a0` / `#6b7280`) |
| lost, unreachable | Lost / Unreachable | red |

Rules that matter: a session the host marks as waiting (or that owns
the pending `/next` ask) shows as *? Need input*. Status is the host's
own (`status` from `/sessions`: starting / working / idle / approval) —
the card no longer infers Stale from activity times or shows context %,
because the device API does not carry them.

## The workflow tracker

There is no separate tracker panel. Under each agent's name, the
status card shows that agent's **workflow**: a short chain of steps,
A → B → C, with a symbol per step — ✓ done, ● current, ○ todo,
✕ blocked. Labels run two or three words so a chain fits one line;
agents without a workflow show nothing.

Epics and work-status tracking deliberately live outside PenguPool
(the owner's rule: that is third-party management), so the chains are
Muse's own curation in `~/workspace/companion/tracker.json` (seeded
from `tracker.example.json` at install):

```json
{"agents": {"<session name>": {"steps": [
  {"label": "…", "status": "done|current|todo|blocked"}]}}}
```

`companion tracker` prints the chains as text.

## Answering agent questions

When a harness needs the owner, there is a signal — the detector reads
the ask straight off the device API (`/next`) and the wake carries it.
No triage: needs-input is relayed, never judged. The answering flow
(full design in [`ANSWER-CARDS.md`](ANSWER-CARDS.md)):

- **Context card** (HTML, display only): the asking agent, the
  question, and a Context block saying what the decision affects —
  `companion ask` renders it from a small JSON spec.
- **Choice card** (the chat's native tappable list): shown only when
  the ask carries options, directly under the context card, containing
  ONLY the choices (plus a "My own answer…" row). **A tap files the
  answer immediately** — a choice is the decision; there is no extra
  confirmation.
- **Open question**: context card only; the owner answers in the
  conversation. If the agent's Muse drafts the wording (anything that
  is not the owner's own words), the exact text is shown and filed
  only after the owner's Send tap. The owner's own words file
  directly.
- "My own answer" settles in conversation; then Muse operates the
  API — selects the proper option for the ask and types the answer in
  — never a terminal side channel.
- Answered asks re-render as a closed card; a question that changes
  (payload hash moved) is re-issued before anything can be filed
  against it.

## The mobile mini site

`src/serve.mjs` is a zero-dependency Node server that runs **on the
PenguPool machine** and serves a phone-first live status page: the team
tree and indicators, a red banner when an agent needs input, auto-refresh
every 10 seconds. It binds only to the machine's Tailscale address, and
reads the paired-device API through this machine's Tailscale Serve name
with its own device token (`~/.muse-companion/device-token`, paired once
under Settings › Muse app). Every page request must carry the site key
(`?key=...`, made by the installer). Install it on the PenguPool machine
with `./install-host.sh` (Node 22+, launchd; survives reboots), then open
the `http://<tailnet-ip>:7790/?key=...` URL it prints on the phone.

## Install

```
./install.sh
```

Installs into the layout the code's paths assume:

```
~/workspace/pengupool-agent/   pp.py detect.py gadget.py tracker.py askcard.py collect_status.py (+ tokens, below)
~/workspace/companion/         companion.py, companion symlink, config.json
~/hooks/scripts/               pengupool-status.sh
```

Then, once:

1. Place the paired device token at `~/workspace/pengupool-agent/device-token`
   (mode 0600). It is the only credential the companion holds: the
   daemon bootstrap token never leaves the workstation, and `/ws`
   would not answer off-machine anyway. **Tokens are never part of
   this package** — they live beside the installed code only, and
   `.gitignore` guards them here. The device token does not rotate on
   daemon restarts.
2. Export `PENGUPOOL_HOST=<workstation tailnet name or IP>` where the
   hook runs (required: calls fail fast without it). When it is an IP,
   also export `PENGUPOOL_HOST_HEADER=<workstation tailnet name>`.
3. Register/enable the `pengupool-status` hook (60-second poll) pointing
   at `~/hooks/scripts/pengupool-status.sh` — in Muse this is a hook
   definition, so ask Muse to set it up.
4. `~/workspace/companion/companion status` to verify.

## Requirements

Python 3 (standard library only), `jq` for the hook wrapper, and
Tailscale reachability from Muse's VM to the workstation's
Tailscale Serve endpoint (HTTPS port 443, or plain HTTP port 80 where
the tailnet has no certificates) — via the runtime tunnel proxy, which
`pp.py` handles.
