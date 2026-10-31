# muse-companion — a companion executive for Muse + PenguPool

The small toolset that lets a Muse (in the Muse app) keep an eye on a
PenguPool team running on the owner's workstation, from the owner's
phone: a status monitor that stays silent unless something material
changes, a CLI that treats the check timer as configuration (never a
code change), and a status card rendered for chat with PenguPool's own
tree and status indicators.

This is the reference implementation behind the repo's
`docs/muse-app-management.md` tutorial.

**Installing with an agent:** point a Muse at this repo and ask it to
set up the companion — [`SETUP.md`](SETUP.md) is the step-by-step it
follows, including the Tailscale setup (the free Personal plan is
enough), the one-time steps only the owner can do, and the checks that
prove the install works.

## Pieces

| File | Role |
| --- | --- |
| `src/pp.py` | Paired-device API client (`/api/muse/v1`): sessions, next, answer, say, action, compass. Token from `./device-token` (0600) or `$PENGUPOOL_DEVICE_TOKEN`, the only credential; host from `$PENGUPOOL_HOST` (required, the workstation's Tailscale Serve name), over HTTPS on port 443 (`$PENGUPOOL_PORT` overrides it). Goes through `$HTTPS_PROXY`'s CONNECT tunnel when set. |
| `src/detect.py` | The change detector. One poll prints one JSON line: `{"decision": "silent"|"wake", ...}`. Wakes only on material change: a session appearing/disappearing, a status change, an ask waiting on the owner (`/next`), a daemon restart (the `/api/healthz` epoch changes), reachability flips. |
| `src/companion.py` | The CLI (below). Owns the monitor config. |
| `src/gadget.py` | Renders the team status card (chat-widget HTML) from live API data. |
| `src/tracker.py` | Per-agent workflow chains (A → B → C with a status symbol per step) rendered under each agent's name in the status card, from Muse's own `tracker.json` (tracking lives outside PenguPool by the owner's rule). |
| `src/askcard.py` | Renders the context card for a pending agent question (and its closed state). See "Answering agent questions". |
| `src/serve.mjs` | The optional mobile mini site (zero-dependency Node; runs on the PenguPool machine). |
| `install-host.sh` | Installs the mini site on the PenguPool machine (launchd). |
| `tracker.example.json` | Seed for the work tracker, copied on first install. |
| `hook/pengupool-status.sh` | The every-minute hook wrapper: runs the detector and forwards its decision. |
| `install.sh` | Installs the layout below; idempotent. |
| `selftest.py` | `python3 -I selftest.py`: checks `pp.py`, the detector and the status card against a stand-in API. |
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

`gadget.py` rebuilds, from `/sessions`, the card Muse shows in chat:
the team as a tap-to-fold tree (each session's team, role and parent;
folding any parent row hides its branch), each agent's workflow chain
under its name, and PenguPool's **own** status indicators — labels
and colours mirror `web/src/app/ctx.tsx` (`STATUS`) and the design
tokens in `web/src/index.css`:

| Status | Label | Colour |
| --- | --- | --- |
| working | Active | green, pulsing (`#15803d` / `#4ade80`) |
| idle | Waiting | amber (`#b45309` / `#fbbf24`) |
| approval | ? Need input | red (`#c0262d` / `#f87171`) |
| ended | Ended | grey (`#8a91a0` / `#6b7280`) |
| lost, unreachable | Lost / Unreachable | red |

A running session with an ask waiting on the owner shows as
*? Need input*. The device API carries no version, context % or
last-active time, so the card shows none.

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
PenguPool machine** and serves a phone-first live status page: the same
tree and indicators as the status card, a red banner when an agent
needs input, auto-refresh every 10 seconds. It binds only to the
machine's Tailscale address and reads the paired-device API with its
own device token (pair it as a device; it never holds the owner's app
token). Every page request needs the site key that `install-host.sh`
makes, so the URL it prints, `http://<machine>.<tailnet>.ts.net:7790/?key=…`,
is the one to open on the phone; keep it to your own devices.

## Install

```
./install.sh
```

Installs into the layout the code's paths assume:

```
~/workspace/pengupool-agent/   pp.py detect.py gadget.py tracker.py askcard.py (+ device token, below)
~/workspace/companion/         companion.py, companion symlink, config.json
~/hooks/scripts/               pengupool-status.sh
```

Then, once:

1. Place the paired device token at `~/workspace/pengupool-agent/device-token`
   (mode 0600). It is the only credential, and it does not rotate.
   **Tokens are never part of this package**: they live beside the
   installed code only, and `.gitignore` guards them here.
2. Export `PENGUPOOL_HOST=<the workstation's Tailscale Serve name>` and
   `PENGUPOOL_PORT=443` where the hook runs: PenguPool is reached only
   through Tailscale Serve, so `pp.py` speaks HTTPS end to end and a proxy
   on the way never sees the device token.
3. Register/enable the `pengupool-status` hook (60-second poll) pointing
   at `~/hooks/scripts/pengupool-status.sh` — in Muse this is a hook
   definition, so ask Muse to set it up.
4. `~/workspace/companion/companion status` to verify.

## Requirements

Python 3 (standard library only), `jq` for the hook wrapper, and
Tailscale reachability from Muse's VM to the workstation's Tailscale Serve
name (HTTPS, port 443) — via the runtime tunnel proxy, which `pp.py` handles.
