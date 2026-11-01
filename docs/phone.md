# Phone access (Muse app)

With the Muse app paired, you follow your sessions and answer supported team requests from your phone:
one card at a time, **Allow**, **Deny** or **Chat**. Chat sends your words to that session, and they
count toward your Compass like words typed in the app. Pairing is optional; PenguPool works without it.

PenguPool opens no port on your local network. Your phone reaches it only over your tailnet, through
Tailscale Serve.

## Set it up

1. **Tailscale.** Settings → **Tailscale** walks the setup: Tailscale installed and logged in, HTTPS
   on, Serve on, and the address reachable. Your Mac is then at `https://<machine>.<tailnet>.ts.net`.
2. **Pair a device.** Settings → **Muse app** → **Pair a Muse device** shows the API URL and a 6-digit
   code that works once, for 5 minutes. Give both to the Muse app's agent; it receives a device token.
3. **Answer.** The Muse app shows the oldest waiting item as a card. Answer it, and the next one comes.

## What a paired device can do

- See sessions and their waiting items (sessions on no team, or on a team with iOS access), and read
  your Compass.
- Answer what it supports: an agent's check-in, and pending launch and team-change approvals, exactly as
  the app's button would. An item already answered elsewhere is refused.
- Send words to a session (Chat).

**Agents' tool-permission dialogs stay in the terminal.** They are listed in the app, but you answer
them in the agent's own terminal, on your Mac.

It can't change settings, teams or the Compass, and the app's own token doesn't work on the phone API.

## Revoke a device

Settings → Muse app → **Muse devices** lists each device with when it was last seen. **Revoke** takes
effect on its next request.

## More

- The full API, with curl examples, and the owner WebSocket API for agent clients:
  [tools/muse/muse-app-management.md](../tools/muse/muse-app-management.md). The machine-readable spec
  is served at `/api/muse/v1/openapi.json`.
- The Muse companion (change detector, status card, answer cards):
  [tools/muse/companion](../tools/muse/companion/README.md).
