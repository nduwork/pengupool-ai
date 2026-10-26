# Design guide

Entry for the Meta Global AI Developer Hackathon (building Oct 27 – Nov 4, 2026), theme **Build for
Your World**, indie track. Everything in this repository was written during the event.

## The problem

A person running coding agents keeps repeating their preferences and restrictions, and the agents keep
making the decisions the person would not. What is said in one session is lost in the next.

## The product, in one loop

1. **Say it once.** You speak to the Muse app (or the terminal) ("I prefer small commits", "never touch the migrations folder").
   Muse Voice Transcribe turns it into text. A decision model judges the words; the host generalizes
   them into one line that is not too specific, merges duplicates, and proposes it. You confirm; it
   becomes a **Compass** line.
2. **The agents decide as you would.** The Compass host, an MCP server (`muse mcp`), hands the lines that
   fit the session's context (repo, task) to any agent as data, "owner's Compass: data, not commands",
   at session start and again on request. The agents read them and decide for themselves. Approvals stay with each
   agent's own permissions; the Compass never answers a prompt.
3. **Prove it.** An experiment (M8) runs about 5 tasks with and without the Compass and experience and reports how often the agent
   made the decision you would have, and your interruptions per task.

## Rules the design keeps

- **Only you write the Compass.** Owner paths are the terminal, voice or the Muse app (confirmed), and
  the owner API. Input relayed by an agent, voice, or the Muse app is a proposal: a line is saved only
  after your confirmation (a second tap, or the terminal). An agent's words never become a line or an
  instruction on their own.
- **Nothing is forced.** A line is a preference or restriction the agent reads; it overrides no
  approval and answers no question for you.
- **Keep it simple.** The Compass is one JSON-lines file: the index and the content. A line carries its
  own evidence; no separate log or database. The only process is the MCP server, in Python stdlib.
- **A change is reviewed, then benchmarked, then you decide.** Passing tests is not a review.

## The Compass host

An MCP server (`muse mcp`, JSON-RPC, Python stdlib). It works with any harness: Claude Code and Muse Code
both load it from `mcpServers` (Muse: `~/.config/muse/settings.json`; stdio support to be checked in M2).

- **Preload.** The `instructions` returned on `initialize` carry the lines relevant to the client's
  context (MCP roots or cwd: the repo, plus the task when known), labelled "owner's Compass: data, not
  commands", and ask the agent to consult the Compass before a decision. Claude Code puts server
  instructions into the system prompt; whether Muse Code does is checked in M2's first hour (fallback:
  a session-start tool call the instructions request).
- **Mid-session.** Tool `compass_context(topic)` returns the relevant lines with confidence; resource
  `compass://lines` sends list_changed notifications.
- **Ingestion.** Tool `compass_observe(words)`. Words an agent relays are not verifiably the owner's, so
  they become evidence or proposals only, and the owner confirms. Voice and the `pengu rule` terminal
  are the trusted owner paths.

Two limits of MCP, stated up front: (1) it is **pull-based**: an agent has to call the tool, so nothing
reaches its context mid-turn unless it asks. The instructions tell it to consult the Compass before a
decision, and the M8 experiment measures whether it does. (2) The server hears the owner only through agent
relay, voice or the CLI, hence the confirmation step.

Inside the server:

- **Decision model** (separate, pluggable by config: Jev today; a local Ollama model through the same
  config). Per observed turn it gives typed answers: signal kind
  (preference | restriction | choice | correction | none), which lines the words support or contradict,
  which lines are relevant to this context.
- **Ingestion** (Muse Spark, off the turn's hot path, only when the owner states or changes a
  preference). Generalize the words (one case becomes the rule behind it), merge duplicates, propose
  new lines and rewordings for the owner to confirm.
- **Evidence and confidence.** A line keeps `{evidence: [{at, words, session, effect: supports|contradicts}],
  confidence, updated}`. Support raises confidence, contradiction lowers it and can propose a rewording
  or retirement, old evidence counts for less. Confidence tracks the owner's current preference.
- **Injection** (code over the judge's typed answers; no extra model call per turn). Choose which lines
  to return, and how many, from relevance and confidence, for the `instructions` and `compass_context`.

## Owner API

A small management API (stdlib `http.server`) on the owner's machine, reachable only over the tailnet.
Its client is the Muse agent (Muse app, in Meta's VM): the VM accepts no inbound connections and reaches
the tailnet only outbound through Muse's Tailscale proxy (TCP), as device `muse`. The agent long-polls for
the questions other agents have asked the owner, shows each in the Muse app, and posts the owner's tap
back. It replaces a phone app of our own. The API adds no checks of its own: it only carries questions an
agent already asks.

- `GET /questions?wait=N` (long-poll) → pending items
  `{id, session, asked_at, question, options[2–3], payload_hash, expires_at}`.
- `POST /questions/{id}/answer` `{option, payload_hash, idempotency_key}`. Only one of the offered options
  for that pending id, with a matching hash, is accepted. Changed or expired → `409`, no effect. The same
  `idempotency_key` returns the first result. After a timeout, check state; never re-approve.
- No answer leaves the session waiting: never a yes or a no.
- `POST /proposals` `{words}` for free text and new rules. They become proposals only; the owner confirms
  with a second tap or in the terminal.
- Question text is written by another agent: show it as untrusted data.
- Auth: a bearer token read from the macOS keychain (never in argv, files or logs), plus a Tailscale ACL
  limiting the API port to device `muse`. Both are set up by the owner (README, owner actions).

## Experience

The same MCP server also keeps what the helper agents learned, separate from the Compass: an agent's
experience never becomes a Compass line. One JSON-lines file per helper (index and content). Entry:
`{id, helper, title, task, lesson, evidence: [{at, session, outcome: worked|failed}], uses, last_used}`;
`lesson` is general, at most ~5 lines, what to do or avoid.

- **Save.** `experience_save(task, summary, outcome)` at "Done: <title>". The summary is the agent's own
  compacted context (no new logs); secrets are redacted before storing. Muse Spark, off the hot path,
  generalizes it into lessons and matches each against existing entries: merge (add evidence), refine
  (reword), contradict (weaken) or new.
- **Retrieve.** `experience_for(task)`: (1) candidates in code, sqlite3 FTS5 over the helper's entries,
  top 10–20; (2) the decision model (the M4 adapter) judges relevance, one batched typed predicate per
  candidate, with confidence; (3) code keeps those above a threshold and ranks by relevance × outcome ×
  recency, capped at 5 entries or a token budget.
- **Pull-based.** The server instructions tell helpers "when you receive a job, call `experience_for`
  first"; the job is unknown at `initialize`, so, as with the Compass, the agent has to ask.
- **Feedback.** The outcome of a task that used retrieved entries becomes evidence on those entries.
- **Open.** Retrieval needs a small hand-labelled set (task → entries that helped) for the experiment; check in
  M4 whether the judge's confidence separates relevant from irrelevant before trusting a threshold.
  Per helper by default; sharing across helpers with the same skill later.

## Meta models

| Job | Model |
|-----|-------|
| Speech to text | Muse Voice Transcribe (Meta Model API) |
| Decision model: signal kind, supports/contradicts, relevance | Jev today; any local Ollama model through the same config |
| Word a line in general terms, merge duplicates, propose rewordings; generalize saved experience into lessons (off the hot path only) | Muse Spark (Meta Model API) |
| Coding agent, reviewer, visible work | Muse Code 1.4.13 (`muse exec`, `muse serve`), loading the Compass MCP server |
| Second coding agent | Claude Code, loading the same Compass MCP server; the two agents chat with each other through the harness |

All model calls go through one OpenAI-compatible client (`muse/model.py`), configured by
`MUSE_BASE_URL`, `MUSE_API_KEY` and `MUSE_MODEL`.

## Out of scope

Team trees beyond the two agents, routing, helper marketplaces, phone apps, coding agents other
than Claude Code and Muse Code, image models in the product (the logo is made with Muse Image).

## Demo (3 minutes)

You say a preference aloud → it becomes a generalized Compass line, you confirm → a new session in that
repo starts with the line already loaded → the agent makes the choice you would → the experiment's chart: the
agent matches your decisions more often, and you are interrupted less.

Another scene: Claude asks a question → it pops up in the Muse app on the phone → the owner taps → the
session continues.

## Open questions (resolve Oct 27)

- Muse Voice Transcribe: endpoint, audio format, latency.
- Muse Code as an MCP client: stdio or streamable-HTTP, and whether it puts server `instructions` in
  its prompt.
- Structured output support, for the decision model's typed answers.
