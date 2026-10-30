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
| `src/pp.py` | Owner-API client: speaks the gateway `/ws` protocol (v1 frames) over the Tailscale tunnel. Token from `./token` (0600) or `$PENGUPOOL_TOKEN`; host from `$PENGUPOOL_HOST` (required), port `$PENGUPOOL_PORT`. |
| `src/detect.py` | The change detector. One poll prints one JSON line: `{"decision": "silent"|"wake", ...}`. Wakes only on material change: a session appearing/disappearing, a status change, a device-visible ask (`/next`), a daemon version change, reachability flips, a stale bootstrap token. |
| `src/companion.py` | The CLI (below). Owns the monitor config. |
| `src/gadget.py` | Renders the team status card (chat-widget HTML) from live API data. |
| `src/tracker.py` | Per-agent workflow chains (A → B → C with a status symbol per step) rendered under each agent's name in the status card, from Muse's own `tracker.json` (tracking lives outside PenguPool by the owner's rule). |
| `src/askcard.py` | Renders the context card for a pending agent question (and its closed state). See "Answering agent questions". |
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

Rules that matter: an idle session with an open check-in gate shows as
*? Need input*; an idle non-helper quiet for over 10 minutes shows as
*Stale* (helpers never go stale); context % is green below 30, amber
below 60, red from 60.

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
machine's Tailscale address, and reads the owner API locally with the
current bootstrap token (read fresh from `hub.token` on every poll, so
daemon restarts need no attention). Install it on the PenguPool machine
with `./install-host.sh` (launchd; survives reboots), then open
`http://<machine>.<tailnet>.ts.net:7790` on the phone.

## Install

```
./install.sh
```

Installs into the layout the code's paths assume:

```
~/workspace/pengupool-agent/   pp.py detect.py gadget.py tracker.py askcard.py (+ tokens, below)
~/workspace/companion/         companion.py, companion symlink, config.json
~/hooks/scripts/               pengupool-status.sh
```

Then, once:

1. Place the daemon bootstrap token at `~/workspace/pengupool-agent/token`
   and the paired device token at `~/workspace/pengupool-agent/device-token`
   (both mode 0600). **Tokens are never part of this package** — they live
   beside the installed code only, and `.gitignore` guards them here.
   Note the bootstrap token rotates at every daemon start; when the
   detector reports it stale, refresh it from the daemon's
   `~/.pengupool-ai/v2/run/hub.token` on the workstation.
2. If the workstation's Tailscale IP differs from the default, export
   `PENGUPOOL_HOST` where the hook runs.
3. Register/enable the `pengupool-status` hook (60-second poll) pointing
   at `~/hooks/scripts/pengupool-status.sh` — in Muse this is a hook
   definition, so ask Muse to set it up.
4. `~/workspace/companion/companion status` to verify.

## Requirements

Python 3 (standard library only), `jq` for the hook wrapper, and
Tailscale reachability from Muse's VM to the workstation's gateway
(port 7787) — via the runtime tunnel proxy, which `pp.py` handles.
