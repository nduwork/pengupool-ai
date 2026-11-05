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
- Answer what it supports: an agent's question, an agent's check-in, and pending launch and team-change
  approvals, exactly as the app's button would. An item already answered elsewhere is refused.
- Send words to a session (Chat).
- Stop, interrupt or resume an agent session.
- Ask for a member to join or leave a team. The request waits for your approval, like a Lead's.

**Agents' tool-permission dialogs stay in the terminal.** They are listed in the app, but you answer
them in the agent's own terminal, on your Mac.

It can't change settings or the Compass, change a team without your approval, start new sessions or
read a session's screen, and the app's own token doesn't work on the phone API.

## Try it

1. Start a **Muse Code** session in a small demo repository (Start in a folder, then pick Muse Code).
2. Ask it to inspect the project and propose one small improvement, and to ask you before changing
   anything with PenguPool's ask command, for example
   `pengupool ask --owner-only --option "Go ahead" --option "Not now" "May I make this change?"`
   (agents are told the command's full path).
3. When the question is waiting, open the Muse app: it shows it as a card with the options to tap.
   Pick one, or reply in your own words.
4. Back on the Mac, the same session receives your answer and goes on. The question stays open for 15
   minutes; each `pengupool ask` call waits up to 2 minutes, then tells the agent to keep waiting with
   `pengupool ask --wait ID`. If your answer arrives while the agent isn't waiting (say its shell tool
   stopped sooner), PenguPool passes it to the session as a notice, and `pengupool ask --wait ID` still
   returns it.

If the agent ends its turn with a plain paragraph instead of a question, nothing waits on you, and
there is nothing to answer from the phone. A tool-permission prompt is also not on the phone; answer it
in the terminal.

## Revoke a device

Settings → Muse app → **Muse devices** lists each device with when it was last seen. **Revoke** takes
effect on its next request.

## More

- The full API, with curl examples, and the owner WebSocket API for agent clients:
  [tools/muse/muse-app-management.md](../tools/muse/muse-app-management.md). The machine-readable spec
  is served at `/api/muse/v1/openapi.json`.
- The Muse companion (change detector, status card, answer cards):
  [tools/muse/companion](../tools/muse/companion/README.md).
