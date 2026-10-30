# Setup — installing the companion, semi-automatically

This guide is written to be executed by a Muse (in the Muse app)
working for the owner, with the owner approving a handful of one-time
steps. End state: Muse can see the owner's PenguPool team from chat,
checks it silently on a timer the owner controls, and speaks up only
when something material changes.

```
workstation (PenguPool)  ──Tailscale──  Muse's VM (this package)
   gateway :7787                          hook → detector → wake/silent
   Muse device API                        companion CLI + status gadget
```

## 0. What only the owner can do (once)

Agents cannot do these; ask for them, then continue automatically.

1. Create/own the Tailscale account and approve new devices in the
   admin console (a browser step).
2. In the PenguPool app on the workstation: Settings → turn **Local
   network** on for the gateway, and leave the app running.
3. Unlock the workstation when a GUI toggle is needed.
4. Make a pair code for each device (Settings › Muse app › Pair a
   Muse device) and approve Muse's prompts when they appear (hook
   registration).

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
3. Make sure the workstation's gateway listens beyond loopback:
   PenguPool Settings → **Local network** on (owner step 0.2). The
   gateway then listens on `[::]:7787` (health check: `http://[::1]:7787`
   from the workstation itself).
4. **No Tailscale Serve, no HTTPS certificates, no public funnel.**
   Since pengupool-ai v0.9.28 the Muse device API accepts requests
   addressed to the machine's own live Tailscale name, so plain HTTP
   inside the tailnet just works. (Before v0.9.28 the API only
   accepted a name saved by the Serve setup flow, and every call
   returned 403.)

## 2. Pair Muse as a device (agent does this)

The companion talks to the gateway only through the paired-device API
(`/api/muse/v1`), with one device token. Pairing:

1. Ask the owner for a pair code: in the PenguPool app, Settings ›
   Muse app › Pair a Muse device. It shows a six-digit code and the
   URL to use.
2. `POST /api/muse/v1/pair` with `{"code": "<code>", "device_name":
   "Muse (agent)"}`; the answer carries the device token.
3. Verify: `GET /api/muse/v1/sessions` with the token → 200;
   without → 401; `GET /api/muse/v1/next` → 204 when nothing waits.

The full pairing walkthrough also lives in the repo's
`tools/muse/muse-app-management.md`.

## 3. The device token (agent places it; it never enters any repo)

Save the device token from step 2 at
`~/workspace/pengupool-agent/device-token` (mode 0600). It is the only
credential the companion uses, and it does not rotate at daemon
restarts; the owner revokes it under Settings › Muse app.

## 4. Install the package (agent)

From a clone of the pengupool-ai repo:

```
tools/muse/companion/install.sh
```

This installs `pp.py`/`detect.py`/`gadget.py` to
`~/workspace/pengupool-agent/`, the CLI to `~/workspace/companion/`,
seeds `config.json` (enabled, every 1m), and installs the hook
wrapper. Export `PENGUPOOL_HOST=<workstation tailnet name or IP>` in
the hook's environment; there is no default. (From a Muse VM, HTTP to the workstation
goes through the runtime's CONNECT tunnel proxy — `pp.py` already
handles this.)

Then register and enable the **pengupool-status** hook (agent, with
owner approval — step 0.4): a 60-second poll running
`~/hooks/scripts/pengupool-status.sh`.

## 5. Verify end-to-end (agent; all must pass)

```
companion status        # monitor enabled, last check, sessions listed
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
owner's phone, install the mini site on the PenguPool machine itself.
It is a paired device of its own: pair it as in step 2 (a device name
such as `Status site`), save its token on that machine, then install:

```
(umask 077; mkdir -p ~/.muse-companion && pbpaste > ~/.muse-companion/device-token)
cd tools/muse/companion && ./install-host.sh   # PORT=7791 changes the port
```

It needs Node 22+ on that machine and nothing else: launchd keeps it
running, it binds to the machine's Tailscale address only, and it reads
`/api/muse/v1/sessions` with that device token; it never holds the
owner's app token. Every request but `/health` needs the site key the
script makes, so open the URL it prints (`…:7790/?key=…`) and keep it
to the owner's devices; anything without the key gets 404. Verify:
`curl http://<machine>.<tailnet>.ts.net:7790/health` answers
`{"ok":true}` from another tailnet device, the page without the key is
404, and with it shows the live team. The site is read-only: names and
statuses, nothing else. Revoking its device under Settings › Muse app
stops it.
