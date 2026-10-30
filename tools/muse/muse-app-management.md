# Managing PenguPool from the Muse app

With this set up, you can hold only a phone, talk to your Muse in the Muse app, and have it manage
this machine's PenguPool sessions: see them, answer the ones waiting on you, and send them words.
An agent client that needs more (reading a session's screen, typing into it) uses the owner
WebSocket API at the end of this guide.

## What you need

- PenguPool AI running on the machine your sessions run on (the "PenguPool machine").
- A [Tailscale](https://tailscale.com) tailnet with the PenguPool machine and the phone (or the
  machine your Muse agent runs on) both on it. The free plan is enough. Check that both show as
  connected under Machines in the Tailscale admin console.

## 1. Turn on Local network

PenguPool AI › Settings › **Local network** › switch it on.

- The host then listens on every interface at port 7787 (`0.0.0.0:7787`, which macOS reports as
  `[::]:7787`) instead of loopback only. Settings › Local network › Bind address sets another
  `host:port` if you need one.
- Turning it on restarts the host. Your sessions keep running, and the app reconnects by itself.
- Settings › Local network › Host address shows where it listens now.

## 2. How the host check works

Every Muse API call names a host in its URL, and PenguPool answers only these names:

- **This machine's own Tailscale name**, for example `http://mac.tail1234.ts.net:7787`. PenguPool
  reads it from Tailscale itself, so there is nothing to save. This works over plain HTTP: the
  tailnet already encrypts the traffic (WireGuard).
- **The Tailscale Serve name**, `https://mac.tail1234.ts.net`, if you set up Serve under
  Settings › Tailscale. Serve stays an alternative. It needs HTTPS certificates enabled in the
  Tailscale admin console.

Any other name, even one that resolves to this machine, gets `403 forbidden host`. That blocks web
pages that try to reach PenguPool through a borrowed DNS name. The host name is not the credential.
The device token is, and every call except pairing needs it.

## 3. Pair a device

1. On the PenguPool machine: Settings › **Muse app** › **Pair a Muse device**. It shows a 6-digit
   code (valid for 5 minutes, one use) and the API URL to give the Muse app, for example
   `http://mac.tail1234.ts.net:7787/api/muse/v1`. The URL's host is the Serve name when Serve is
   set up, otherwise this machine's Tailscale name. If it shows `https://<tailnet-name>`, Serve is
   not set up and either Local network is off (do step 1 first), Bind address names one address
   instead of every interface, or Tailscale is not running on this machine.
2. In the Muse app, give your Muse the URL and the code. It pairs with:

   ```bash
   API=http://mac.tail1234.ts.net:7787/api/muse/v1
   curl -s -X POST "$API/pair" -H 'Content-Type: application/json' \
     -d '{"code":"123456","device_name":"My phone"}'
   # {"token":"9f2c…"}
   ```

   The token is shown once. PenguPool keeps only its SHA-256 hash (in `config.json` in its store),
   so a lost token cannot be shown again: pair again under a new name.
3. The device now appears under Settings › Muse app with when it was last seen.

Pairing rules: each device name pairs once (`409` for a name already paired; the code is used up
by that try, so make a new one). A wrong or expired
code is `401`. After 10 wrong codes, pairing is paused for a minute (`503`, `Retry-After: 60`).

**Revoke** a device under Settings › Muse app › Revoke. Its token stops working on its next call.

An agent client with the owner's token can also make a code itself with the `muse.pairCode`
request (see [The owner WebSocket API](#the-owner-websocket-api)). It answers `{code, url}`.

## 4. The device API

Every call carries the token: `Authorization: Bearer <token>`. Set this up once:

```bash
API=http://mac.tail1234.ts.net:7787/api/muse/v1
AUTH="Authorization: Bearer 9f2c…"
```

**List sessions.**

```bash
curl -s "$API/sessions" -H "$AUTH"
# [{"session":"01a1…","title":"api-fix · backend","state":"running","waiting":true,
#   "question":"Bash: git push origin main"}, …]
```

`waiting` is true when the session has an ask waiting on you. `question`, when present, is what
the agent is asking in its terminal right now (a permission, a question with its choices, a plan
to approve). It is answered in the terminal, not here; it goes away once the agent moves on.

**Get the next waiting ask.** It returns one card, for the oldest ask. `wait` (0–60 seconds) holds
the call open until an ask arrives. `204 No Content` means nothing is waiting.

```bash
curl -s "$API/next?wait=60" -H "$AUTH"
# {"session":"01a1…","team":"…","title":"api-fix · backend",
#  "description":"Start the team? …","ask_id":"gate:42","payload_hash":"5e1d…",
#  "actions":[{"id":"allow","label":"Allow"},{"id":"deny","label":"Deny"},{"id":"chat","label":"Chat"}]}
```

**Answer an ask** with `allow` or `deny`:

```bash
curl -s -X POST "$API/asks/gate:42/answer" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"action":"allow","payload_hash":"5e1d…","idempotency_key":"tap-7f3a"}'
```

- `payload_hash` is the card's. If the ask changed since you read it, or someone already answered
  it, the answer is refused with `409`: read `/next` again.
- `idempotency_key` is any unique string per tap. Retrying with the same key returns the first
  answer and never answers twice. A retry that arrives while the first is still running gets
  `503` with `Retry-After: 1`. Reusing a key for a different ask or action is `409`.

**Answer an agent's own question.** An agent can put a question to you itself
(`pengupool ask`, below). Its card has `ask_id` `ask:…`, and its actions are the agent's choices
(`option:0`, `option:1`, …) followed by `reply`:

```bash
curl -s -X POST "$API/asks/ask:01a1…/answer" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"action":"option:1","payload_hash":"5e1d…","idempotency_key":"tap-81c2"}'
curl -s -X POST "$API/asks/ask:01a1…/answer" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"action":"reply","text":"Tomorrow, after the review.","payload_hash":"5e1d…","idempotency_key":"tap-81c3"}'
```

The agent, waiting on its question, gets your answer and goes on. The same payload-hash,
idempotency and `409` rules apply.

**Say something to a session** ("Chat" on a card, or any time):

```bash
curl -s -X POST "$API/sessions/01a1…/say" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"words":"use the staging database for this"}'
# {"sent":"01a1…"}
```

The words are typed into the session right away, as one line followed by Enter, and count as
yours, exactly as if you had typed them in the app. So the Compass can learn your preferences from
them. A session that is not running, or not visible to the device, is `404`; a running session
that is not an agent (a plain terminal) is `403`.

**Read your Compass** (the lines PenguPool shows your agents):

```bash
curl -s "$API/compass" -H "$AUTH"
# ["Ship small changes first, one feature per release.", …]
```

The full contract is at `GET $API/openapi.json` (no token needed).

## Agents asking you directly

Inside a session PenguPool runs, an agent can ask you a question only you can settle:

```bash
pengupool ask --option Postgres --option SQLite "Which database for the cache?"
# {"answer":"SQLite","by":"My phone","id":"01a1…"}
```

It shows in the app's Approvals (pick an option, or reply in your own words) and as a card on
`/next`. Only you, in the app or from a paired device, can answer it; another agent cannot. The
command waits up to 2 minutes; if you haven't answered by then it prints `{"pending":"<id>"}`, and
`pengupool ask --wait <id>` waits again. An agent has at most 3 asks waiting at once. An ask you leave unanswered
for 15 minutes expires: its card leaves `/next`, a late answer gets `409`, and the agent is told
(`{"expired": …}` from its wait, or a notice) to go on with a safe default. An ask also goes away
when its session ends. An answer given while the agent isn't waiting reaches it
as a notice at its next turn.

## 5. What a paired device can and cannot do

A device token can list sessions (with each one's team, role, parent and assignment), read and
answer waiting asks (agents' own questions, and a member's ask the team left to you: go on,
reply or dismiss), send words to a session, stop, interrupt or resume an agent session
(`POST /sessions/{id}/action`), ask for a member to join or leave a team (`POST
/teams/{id}/members`: it waits on `/next` for your allow, like a LEAD's request), and read the Compass. It sees sessions on no team, and teams with **Allow iOS access** switched on (team
sheet on the map).

A device token cannot read a session's screen, start new sessions, change settings, or pair
other devices. It works nowhere but `/api/muse/v1`, and the app's own token never works there.
An agent's permission prompts stay in its terminal: the Muse app answers PenguPool's asks (team
launches, check-ins), not the agent's own tool prompts.

## The owner WebSocket API

An agent client that needs more than the device API (a screen to read, keys to type) uses the
same WebSocket the PenguPool app uses. It takes **the owner's token**, which is full control of
this PenguPool, so keep it on machines you own. On the PenguPool machine, `pengupool-ai web`
prints the app URL, and the token is the part after `#token=`.

This API answers only on the PenguPool machine itself (or through an SSH port forward to it).
Over the network, the tailnet name, Serve or the local network, only the device API and
`/api/healthz` answer; everything else is `404`.

Connect to `ws://127.0.0.1:7787/ws?token=<owner token>` on the PenguPool machine. Every WebSocket message is one binary frame: a 17-byte big-endian header, then the
payload.

| Bytes | Field | Meaning |
| --- | --- | --- |
| 0 | type | 1 control (JSON), 2 output, 3 input, 4 ack, 5 snapshot |
| 1–4 | channel | 0 for control; the attach channel for terminal data |
| 5–12 | seq | your own counter on control and input; a byte offset on output and ack |
| 13–16 | length | payload length |

Control messages are JSON objects with an `id` you pick and a `type`. The first one must be
`hello`:

```json
{"id":"1","type":"hello","v":1,"client":{"kind":"cli","name":"my-agent"}}
```

Then:

- `{"id":"2","type":"session.list"}` returns `sessions`, each with `id`, `name`, `state`, `status`
  (`working`, `waiting`, `approval`, `idle`), `question` (what the agent asks in its terminal now,
  if anything), `team_id` and more.
- `{"id":"3","type":"state.subscribe"}` sends a `state.snapshot` (sessions plus teams), then a
  `state.delta` whenever something changes.
- **App requests** go as `{"id":"4","type":"<request>","params":{…}}` and are answered with the same
  `type` and a `result`, or with `{"type":"error","code":…,"message":…}`. For example:
  `settings.get`, `settings.save` (`{"lan":true,"restart":true}`), `muse.pairCode`, `muse.devices`
  and `muse.revoke` (`{"name":"My phone"}`).
- **Read a session's screen.** Send `{"id":"5","type":"session.attach","session_id":"01a1…","channel":1}`.
  You get `session.attached`, then `snapshot.begin`, one or more snapshot frames (type 5, channel 1)
  that redraw the current screen as terminal bytes, and `snapshot.end`. After that, output frames
  (type 2) stream what the session prints. To keep the stream flowing, send an ack frame now and
  then: type 4, the same channel, `seq` set to the offset you have read up to (the last output
  frame's `seq` plus its length), and an empty payload. Without acks the session pauses your
  stream once you fall behind. `{"type":"session.detach","session_id":"01a1…"}` stops it.
- **Type into a session** while attached: send input frames (type 3, the attach channel) whose
  payload is the bytes to type. **Send the text and the Enter (`\r`) as two frames with a short
  pause between them.** An agent's terminal app takes one large chunk as a paste, and a carriage
  return inside a paste never submits. Your typing counts as the owner's, as in the app.

A minimal client (Node 22 or later, no packages) that lists the sessions and prints the first
running one's screen:

```js
// node list-and-read.mjs http://127.0.0.1:7787 <owner token>
const [base, token] = process.argv.slice(2);
const ws = new WebSocket(base.replace(/^http/, "ws") + "/ws?token=" + token);
ws.binaryType = "arraybuffer";
let seq = 0n;
function send(type, payload, channel = 0) {
  const f = new Uint8Array(17 + payload.length), v = new DataView(f.buffer);
  v.setUint8(0, type); v.setUint32(1, channel); v.setBigUint64(5, seq++); v.setUint32(13, payload.length);
  f.set(payload, 17);
  ws.send(f);
}
const control = (msg) => send(1, new TextEncoder().encode(JSON.stringify(msg)));
ws.onopen = () => control({ id: "1", type: "hello", v: 1, client: { kind: "cli", name: "list-and-read" } });
ws.onmessage = (e) => {
  const b = new Uint8Array(e.data), payload = b.slice(17);
  if (b[0] === 5) return process.stdout.write(new TextDecoder().decode(payload)); // the screen
  if (b[0] !== 1) return;
  const m = JSON.parse(new TextDecoder().decode(payload));
  if (m.type === "error") { console.error(m.code, m.message); return ws.close(); }
  if (m.type === "hello.ok") control({ id: "2", type: "session.list" });
  if (m.type === "session.list") {
    for (const s of m.sessions ?? []) console.log(s.id, s.state, s.status ?? "", s.name);
    const s = (m.sessions ?? []).find((x) => x.state === "running");
    if (s === undefined) return ws.close();
    control({ id: "3", type: "session.attach", session_id: s.id, channel: 1 });
  }
  if (m.type === "snapshot.end") ws.close();
};
ws.onclose = () => process.exit(0);
```

To type into the attached session, add:

```js
const typeLine = async (text) => {
  send(3, new TextEncoder().encode(text), 1);
  await new Promise((r) => setTimeout(r, 300));
  send(3, new TextEncoder().encode("\r"), 1);
};
```

## Security in plain words

- **Transport.** Calls travel inside your tailnet, which WireGuard encrypts end to end, whether you
  use plain HTTP on the Tailscale name or HTTPS through Serve. PenguPool never uses Tailscale
  Funnel, so nothing is exposed to the public internet.
- **Credential.** A device token is the only thing that lets a device in. Each device has its own,
  stored only as a hash, revocable any time under Settings › Muse app.
- **Limits.** A device answers asks, sends words, stops, interrupts or resumes an agent session,
  and asks for team changes you approve. It cannot read screens, run commands, start new sessions,
  or change settings. The owner's token, used by the WebSocket API above, can do everything the
  app can, and it works only on the PenguPool machine.
- **With Local network on,** other machines on your local network can reach port 7787 as well,
  but only the device API and the health check answer there. The PenguPool app, its WebSocket
  and every other route answer only on this machine.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `403 forbidden host` | The URL's host is not this machine's Tailscale name or Serve name: an IP, a different name, a typo. | Use the URL that Pair a Muse device shows. `tailscale status` on the PenguPool machine shows its name. |
| Connection refused or times out on port 7787 | Local network is off: the host listens on loopback only. | Settings › Local network on (step 1), or set up Serve and use its HTTPS URL. |
| Pair a Muse device shows `https://<tailnet-name>` | Serve is not set up, and Local network is off, Bind address names one address, or Tailscale is not running here. | Step 1 (leave Bind address empty), check `tailscale status`, then make a new code. |
| `401 unauthorized` | Token missing, mistyped, or revoked; for `/pair`, a wrong or expired code. | Check the `Authorization: Bearer` header. Pair again for a revoked device, or make a new code. |
| `409` on pairing | A device with that name is already paired. | Make a new code, and pair under another name or revoke the old one first. |
| `409` on an answer | The ask changed or was already answered. | Read `/next` again. |
| `503` with `Retry-After` | A tap still running (1 s), or pairing paused after wrong codes (60 s). | Retry after the stated time. |
| The desktop app shows Reconnecting… after turning on Local network | An older build published the wildcard address to the app. | Update PenguPool AI: the host now publishes a loopback address. If it persists, `pengupool-ai web` shows the address the app uses. |
| The phone was never asked to approve, or the device doesn't appear | Tailscale has not approved the device, or the approval link lapsed. | In the Tailscale admin console, under Machines, check that the device is there, approved and connected. |
