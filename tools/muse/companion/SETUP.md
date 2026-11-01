# Setup — installing the companion, semi-automatically

This guide is written to be executed by a Muse (in the Muse app)
working for the owner, with the owner approving a handful of one-time
steps. End state: Muse can see the owner's PenguPool team from chat,
checks it silently on a timer the owner controls, and speaks up only
when something material changes.

```
workstation (PenguPool)  ──Tailscale──  Muse's VM (this package)
   daemon loopback :7787                    hook → detector → wake/silent
   ← Tailscale Serve (https)                companion CLI + status gadget
   Muse device API
```

## 0. What only the owner can do (once)

Agents cannot do these; ask for them, then continue automatically.

1. Create/own the Tailscale account and approve new devices in the
   admin console (a browser step).
2. In the PenguPool app on the workstation: complete the **Tailscale
   Serve** setup (Settings), so the machine's tailnet name forwards
   to the daemon, and leave the app running. (Since PenguPool
   v0.10.20 the daemon binds loopback only; there is no Local
   network toggle anymore.)
3. Unlock the workstation when a GUI toggle is needed.
4. Approve Muse's prompts when they appear (hook registration,
   device pairing on the Muse side).

Everything else in this guide is agent work.

## 1. Tailscale — the network between the two machines

The gateway is never exposed to the public internet. Both machines
join one private tailnet; the companion reaches the workstation only
inside it.

**Free or paid?** The free **Personal** plan is enough: it allows up
to 3 users and 100 devices, and this setup needs exactly three
devices (owner's phone/computer, the workstation, Muse's VM). Paid
tiers (Personal Plus, Starter, Premium) raise user/device limits and
add admin features — none are required here. If a trial expires, the
tailnet drops to the free plan; devices stay joined.

Steps:

1. Install Tailscale on the workstation and sign in.
   Note its tailnet name (`<machine>.<tailnet>.ts.net`) and IP
   (`100.x.y.z`) — `tailscale status` shows both.
2. Join Muse's VM to the same tailnet when Muse offers it, and
   approve the new device in the admin console (owner step 0.1).
3. Make sure Tailscale Serve is up on the workstation (owner step
   0.2): it terminates HTTPS at the machine's tailnet name
   (`https://<machine>.<tailnet>.ts.net`) and forwards to the daemon
   on loopback `127.0.0.1:7787`. Nothing is exposed publicly —
   Serve is tailnet-only unless a funnel is explicitly enabled;
   never enable the funnel for this.
4. The companion speaks to that Serve endpoint by default
   (`PENGUPOOL_TRANSPORT=serve` in `pp.py`): CONNECT to the
   workstation's tailnet address on port 443 through the runtime
   proxy, TLS with SNI = the tailnet name (Serve's certificate is
   publicly trusted for the name), Host = the tailnet name.
   `PENGUPOOL_TRANSPORT=direct` keeps the pre-v0.10.20 path (plain
   HTTP to a daemon that still binds `[::]:7787`) for older builds.
5. **HTTP is a first-class alternative, not a fallback.** The app
   offers Serve over HTTPS and Serve over plain HTTP (`--http`
   mode) to every user, with or without any subscription; the two
   can run at the same time. Simplest: set `PENGUPOOL_HOST` to the
   pairing URL the app shows (`https://…` or `http://…`); its scheme
   picks `serve` or `serve-http`. `PENGUPOOL_TRANSPORT` still
   overrides it. The tailnet
   itself is WireGuard-encrypted and the device token remains the
   credential; the device API's host check is unchanged.

## 2. Pair Muse as a device (agent does this, owner starts it)

The companion talks to the gateway one way: the paired-device API
(`/api/muse/v1`). It is the companion's only channel — the gateway's
`/ws` owner surface is local-only (the host guard 404s non-loopback
requests for everything except `/api/healthz` and the device API), so
nothing off the workstation can use it.

Pairing:

1. The owner starts pairing in the PenguPool app (Settings → Muse
   app) and gives Muse the one-time pair code.
2. `POST /api/muse/v1/pair` with the code and a device name
   (e.g. `Muse (agent)`) to receive the device token.
3. Save the device token at `~/workspace/pengupool-agent/device-token`
   (mode 0600). It does **not** rotate on daemon restarts.
4. Verify: `GET /api/muse/v1/sessions` with the token → 200;
   without → 401; `GET /api/muse/v1/next` → 204 when nothing waits.

The full pairing walkthrough also lives in the repo's
`tools/muse/muse-app-management.md`.

## 3. Tokens (agent places them; they never enter any repo)

1. **Device token**: from step 2, at
   `~/workspace/pengupool-agent/device-token` (mode 0600). It is the
   only credential on the companion machine.
2. **Bootstrap token**: never copied anywhere. It stays in
   `~/.pengupool-ai/v2/run/hub.token` on the workstation, where local
   clients (the app) read it. The
   device API rejects it by design (the mini site in §7 uses its own
   device token instead), and `/ws` does not answer off-machine.

## 4. Install the package (agent)

From a clone of the pengupool-ai repo:

```
tools/muse/companion/install.sh
```

This installs `pp.py`/`detect.py`/`gadget.py` to
`~/workspace/pengupool-agent/`, the CLI to `~/workspace/companion/`,
seeds `config.json` (enabled, every 1m), and installs the hook
wrapper. Export `PENGUPOOL_HOST=<workstation tailnet name or IP>` for
the hook's environment (required: calls fail fast without it). When it
is an IP, also export `PENGUPOOL_HOST_HEADER=<workstation tailnet
name>` — the header defaults from the host and drives the TLS SNI and
certificate check. (From a Muse VM, HTTP to the workstation goes
through the runtime's CONNECT tunnel proxy — `pp.py` already handles
this.)

Then register and enable the **pengupool-status** hook (agent, with
owner approval — step 0.4): a 60-second poll running
`~/hooks/scripts/pengupool-status.sh`.

## 5. Verify end-to-end (agent; all must pass)

```
companion status        # monitor enabled, daemon epoch, sessions listed
companion check         # one forced pass: "silent — no material change"
companion config set interval 1h && python3 ~/workspace/pengupool-agent/detect.py
                        # → {"decision": "silent", "reason": "not due ..."}
companion config set interval 1m   # restore the owner's cadence
companion gadget        # renders the team card (tree + PenguPool's indicators)
companion tracker       # renders the work-tracker card
companion ask <spec>    # renders a question's context card (ANSWER-CARDS.md)
```

Plus a wake test: make a session appear/disappear (or wait for a real
change) and confirm exactly one short alert reaches the owner's chat,
and that routine polls stay silent.

## 6. Day-to-day

- The owner sets the cadence in words ("check every 5m") — the agent
  runs `companion config set interval 5m`. No code changes, ever.
- The owner says "status" — the agent runs `companion gadget` and
  shows the card.
- The owner says "tracker" — the workflows are part of the status card
  (`companion gadget`); `companion tracker` prints the chains as text.
  Update `tracker.json` first for anything that changed: per agent, a
  short chain of steps (label + status), nothing more.
- Alerts are single and factual: what changed, and the card when it
  helps. Silence is the default state.
- **Needs-input is a signal, not a judgment call.** When a wake carries
  an ask, relay it to the owner as-is (which session, what it is
  asking). The only check allowed: the ask is still open. Never decide
  whether it deserves the owner's attention.
- Answering follows the card flow (see `ANSWER-CARDS.md`): context
  card for the question, the native choice card with only the choices
  when the ask has options, a choice tap files immediately, and a
  "send it" confirmation only for free text you formulated that is not
  the owner's own words.
- The owner answers an ask — you file it through the ask's own answer
  path (the device API answer endpoint for host asks; the session's
  channel for agent questions) and show the closed card.

## 7. Optional: the mobile mini site (agent, on the PenguPool machine)

The chat status card is a snapshot. For an always-live view on the
owner's phone, install the mini site on the PenguPool machine itself:

```
cd tools/muse/companion && ./install-host.sh   # PORT=7791 changes the port
```

It needs Node 22+ on that machine and nothing else: launchd keeps it
running and it binds to the machine's Tailscale address only. Pair it
once under Settings › Muse app and save its device token at
`~/.muse-companion/device-token`; the site reads the paired-device API
through this machine's Tailscale Serve name (`MC_API=https://<name>`
overrides it). Every page request must carry the site key the installer
made (`?key=...`); without it the site answers 404. Verify:
`curl http://<tailnet-ip>:7790/health` answers `{"ok":true}` from
another tailnet device without any key, and the printed page URL shows
the live team. The site is read-only: names and statuses, nothing else.
