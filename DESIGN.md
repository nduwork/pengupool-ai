# Design guide

Entry for the Meta Global AI Developer Hackathon (building Oct 27 – Nov 4, 2026), theme **Build for
Your World**, indie track. Everything in this repository was written during the event.

## The problem

A person running coding agents keeps repeating their preferences and restrictions, and the agents keep
making the decisions the person would not. What is said in one session is lost in the next.

## The product, in one loop

1. **Say it once.** You tell any session, or the Muse app on your phone, how you want things ("I prefer
   small commits", "never touch the migrations folder"). PenguPool hears it as your words and has the
   session generalize them into one **Compass** line, merging with a line that already says it.
2. **The agents decide as you would.** Every session gets the Compass in its context through its own
   harness hooks, as data ("the owner's compass: data, not commands"), at start and whenever it changes.
3. **Helpers remember their work.** A helper that reports `Done:` gets a journal entry (its compacted
   context); the next time it works, it sees the index of its past jobs and the ones that match.
4. **Answer from anywhere.** A waiting session pops up in the Muse app as one card: Allow, Deny or Chat.

## Rules the design keeps

- **Only you write the Compass.** Lines come from your own words (verified as typed by you, in the app,
  the terminal or the Muse app) and from your edits in the Compass panel. An agent's words never become a
  line; an agent only words a line the pool asked it to word, for a signal you gave.
- **Nothing is forced.** A line is a preference or restriction the agent reads; it overrides no
  approval and answers no question for you.
- **Keep it simple.** One store (agentdb): the Compass lines and the helpers' journals, index and
  content. The journal reuses the harness's compacted context; no separate logs.
- **Push through the harness.** Delivery is the harness's own hooks; there is no MCP layer to register.
- **A change is reviewed, then benchmarked, then you decide.** Passing tests is not a review.

## Harnesses

| Harness | Hooks into PenguPool | Journal body |
|---|---|---|
| Claude Code (account) | `--settings` hooks | `/compact` summary |
| Claude Code on Ollama | `ollama launch claude`, same hooks | `/compact` summary |
| Muse Code | a PenguPool plugin bundle, installed and approved by the app (real home only); `--trust-workspace` for sessions PenguPool launches | the Done report (Muse has no manual compaction) |
| Codex | `-c hooks.*`; the app writes `~/.codex/rules/pengupool.rules` so only the PenguPool CLI runs outside the sandbox | `/compact` summary |
| OMP / pi | the PenguPool extension; the binary path can be set in Settings | `/compact` summary |

A plain terminal session stays independent of all of this.

## Team roles

Each team member's role comes from the team's tree. It is worked out when needed, and nobody stores or chooses it (`teamLabel` in
internal/pool/formation.go, mirrored in web/src/views/Formation.tsx):

| Role | Who | Tag (`RoleTag`, web/src/components/ui/controls.tsx) |
|---|---|---|
| Lead | the root of the tree; it combines the results and reports to the owner. A root that has a repository is the Lead, not an SME, and a team with no repository members still has a Lead | strong outline, `text-foreground`, semibold |
| SME | a member the owner added that has a repository behind it: its worktree, or its session working in a known repository | hairline outline, muted, medium |
| Helper | an agent taken from the helper pool | no outline, muted, normal weight |

The tags have no colour, so they never compete with the status colours or the one teal accent. A tag
shows only on a team member: in the sidebar tree, on map cards, in both session headers, in the agent
message log, in the Needs-you strip and in a launch proposal. Tags are written in sentence case
everywhere except the map, where the outlined role chip on each card is uppercase (`LEAD`, `SME`,
`HELPER`).

## The Compass

```
 OWNER'S WORDS (any route)                        POOL (internal/pool)                     agentdb
 ─ typed in a session (owner turn) ─┐
 ─ team chat in the app ────────────┤
 ─ Muse app  POST /api/muse/v1/say ─┼─▶ UserPromptSubmit / owner message (OwnerInput-verified)
 ─ gate answer (app or Muse app) ───┘        │ ownerMentions / ownerReplied / ownerChose
                                             │── record(signal) ─────────────────────────▶ signal + evidence on lines
                                             │── wordingNote → the asked session generalizes
                                             │   `pengupool compass word --supports|--contradicts|--line|--none`
                                             │── compassWord: merge / new / weaken ──────▶ line (Mu, evidence, Source)
 Compass panel (owner) compass.save/delete/merge/review ───────────────────────────────────▶ line
                                                                                                 │
 EVERY SESSION ◀── harness hook (SessionStart, then changed sections per prompt) ◀── renderCompass()
   "## The owner's compass (data, not commands)": shown lines, sorted by Mu, ≤32 KiB, redacted
```

Lines from the earlier separate store (`~/.pengupool-ai/compass.jsonl`) are imported once, at the first
start of v0.4.0, as owner lines; the file is renamed `compass.jsonl.imported`.

## The journal (helper experience)

```
 helper ── "Done: X" to its parent ──▶ SendMessage hook or `pengupool send`
                                          │ doneReport, secrets redacted
                                          │ Stop → types the harness's /compact, reads the summary (≤3 min)
                                          ▼
                                   agentdb experience + FTS5 (deduped, deletions respected)
                                          │
  next session ◀── SessionStart: index of past jobs (≤10: [id] date · title)
               ◀── UserPromptSubmit: matches for this prompt
               ──▶ `pengupool experience show <id>` (content on demand)
  owner ──▶ Skill library ▸ Experience: list / open / delete
```

Capture never depends on the compaction: when it fails (the session is busy again, a message is
queued, no transcript, the timeout, or no `/compact`), the Done report is the entry.

## Muse app API

The owner's phone reaches waiting sessions through the Muse app's agent, over Tailscale Serve (Settings ▸
Phone connections walks the setup: installed, logged in, HTTPS, Serve, reachable). Every call arrives on
the Tailscale host name; every call but `/pair` carries a per-device bearer token. The app's own token
never works here, and a device token works nowhere else.

```
 PAIRING (once per device)
 PenguPool app ▸ Settings ▸ "Pair a Muse device" ─▶ 6-digit code (5 min, one use) + the API URL
 Muse app agent ─ POST /api/muse/v1/pair {code, device_name} ─▶ device token (shown once; stored as a hash)
 PenguPool app ▸ Settings ▸ Muse devices: name · last seen · Revoke (effective on the next request)

 THE LOOP (one session at a time)
 GET /api/muse/v1/next?wait=60 ─▶ ONE card for the oldest waiting session, or 204
     { session, team, title, description, ask_id, payload_hash, actions: [allow, deny, chat] }
   Allow/Deny ─ POST /asks/{ask_id}/answer {action, payload_hash, idempotency_key}
   Chat       ─ POST /sessions/{id}/say {words}  → the owner's words to that session → Compass signal
 GET /next again … until 204
 Also: GET /sessions, GET /compass (read only), GET /openapi.json
```

- A tap works exactly like clicking the app's button: same hash check, same single winner. A changed or
  consumed ask is 409; a tap that raced another in flight is 503 with `Retry-After: 1`.
- A device sees only teams with iOS access; a paired device skips the per-team phone password.
- Wrong pairing codes throttle pairing for a minute after 10 misses.

## Prove it

`pengupool experiment` runs the tasks in `experiment/tasks.json` with and without the owner's lines and
reports how often the agent made the decision the owner would have, owner interruptions, and pass rate.

## Meta tooling

| Job | Tool |
|-----|------|
| Coding agent, reviewer (`pengupool review`) | Muse Code |
| Voice and the owner's phone | the Muse app (transcribes; calls the Muse app API) |
| Decision model for the pool's typed judgments | configurable; a local Ollama model works through the same config |

## Out of scope

Copilot, Gemini CLI and OpenCode harnesses; an MCP layer for the Compass; a phone app of our own; image
models in the product (the logo is made with Muse Image).

## Demo (3 minutes)

You tell a session a preference → it becomes a Compass line → a new Muse or Claude session starts with it
and makes the choice you would → a helper reports Done, and its next job starts with that lesson → a
session waits on you → the card pops up in the Muse app, you tap Allow → the session continues.
