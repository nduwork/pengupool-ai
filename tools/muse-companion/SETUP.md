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

The companion talks to the gateway two ways (see README): the owner
`/ws` API for full reads, and the paired-device API
(`/api/muse/v1`) for the pending-ask signal. Pairing:

1. Over `/ws` (bootstrap token, step 3.1): call `muse.pairCode` to get
   a pair code, then `POST /api/muse/v1/pair` with a device name
   (e.g. `Muse (agent)`) to receive the device token.
2. Save the device token at `~/workspace/pengupool-agent/device-token`
   (mode 0600). It does **not** rotate on daemon restarts.
3. Verify: `GET /api/muse/v1/sessions` with the token → 200;
   without → 401; `GET /api/muse/v1/next` → 204 when nothing waits.

The full pairing walkthrough also lives in the repo's
`docs/muse-app-management.md`.

## 3. Tokens (agent places them; they never enter any repo)

1. **Bootstrap token** (for `/ws`): the daemon writes it to
   `~/.pengupool-ai/v2/run/hub.token` on the workstation. Copy its
   contents to `~/workspace/pengupool-agent/token` (mode 0600) on the
   VM. ⚠️ It **rotates at every daemon start** — when the detector
   reports "bootstrap token stale", refresh it from that file.
2. **Device token**: from step 2, at
   `~/workspace/pengupool-agent/device-token` (mode 0600).

## 4. Install the package (agent)

From a clone of the pengupool-ai repo:

```
tools/muse-companion/install.sh
```

This installs `pp.py`/`detect.py`/`gadget.py` to
`~/workspace/pengupool-agent/`, the CLI to `~/workspace/companion/`,
seeds `config.json` (enabled, every 1m), and installs the hook
wrapper. If the workstation's Tailscale IP is not the default
`100.106.40.12`, export `PENGUPOOL_HOST=<workstation tailnet IP>` for
the hook's environment. (From a Muse VM, HTTP to the workstation
goes through the runtime's CONNECT tunnel proxy — `pp.py` already
handles this.)

Then register and enable the **pengupool-status** hook (agent, with
owner approval — step 0.4): a 60-second poll running
`~/hooks/scripts/pengupool-status.sh`.

## 5. Verify end-to-end (agent; all must pass)

```
companion status        # monitor enabled, daemon version, sessions listed
companion check         # one forced pass: "silent — no material change"
companion config set interval 1h && python3 ~/workspace/pengupool-agent/detect.py
                        # → {"decision": "silent", "reason": "not due ..."}
companion config set interval 1m   # restore the owner's cadence
companion gadget        # renders the team card (tree + PenguPool's indicators)
```

Plus a wake test: make a session appear/disappear (or wait for a real
change) and confirm exactly one short alert reaches the owner's chat,
and that routine polls stay silent.

## 6. Day-to-day

- The owner sets the cadence in words ("check every 5m") — the agent
  runs `companion config set interval 5m`. No code changes, ever.
- The owner says "status" — the agent runs `companion gadget` and
  shows the card.
- Alerts are single and factual: what changed, and the card when it
  helps. Silence is the default state.
