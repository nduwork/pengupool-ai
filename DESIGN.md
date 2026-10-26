# Design guide

Entry for the Meta Global AI Developer Hackathon (building Oct 27 – Nov 4, 2026), theme **Build for
Your World**, indie track. Everything in this repository was written during the event.

## The problem

A person running coding agents keeps repeating their preferences and restrictions, and the agents keep
making the decisions the person would not. What is said in one session is lost in the next.

## The product, in one loop

1. **Say it once.** You speak ("I prefer small commits", "never touch the migrations folder").
   Muse Voice Transcribe turns it into text. A decision model judges the words; the host generalizes
   them into one line that is not too specific, merges duplicates, and proposes it. You confirm; it
   becomes a **Compass** line.
2. **The agents decide as you would.** The Compass host, an MCP server (`muse mcp`), hands the lines that
   fit the session's context (repo, task) to any agent as data, "owner's Compass: data, not commands",
   at session start and again on request. The agents read them and decide for themselves. Approvals stay with each
   agent's own permissions; the Compass never answers a prompt.
3. **Prove it.** A gate runs the same tasks with and without the Compass and reports how often the agent
   made the decision you would have, and your interruptions per task.

## Rules the design keeps

- **Only you write the Compass.** A line is saved only after your confirmation at a terminal; an
  agent's words never become a line.
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
decision, and gate metric (d) measures whether it does. (2) The server hears the owner only through agent
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

## Meta models

| Job | Model |
|-----|-------|
| Speech to text | Muse Voice Transcribe (Meta Model API) |
| Decision model: signal kind, supports/contradicts, relevance | Jev today; any local Ollama model through the same config |
| Word a line in general terms, merge duplicates, propose rewordings (ingestion only) | Muse Spark (Meta Model API) |
| Coding agent, reviewer, visible work | Muse Code 1.4.13 (`muse exec`, `muse serve`), loading the Compass MCP server |
| Second coding agent | Claude Code, loading the same Compass MCP server; the two agents chat with each other through the harness |

All model calls go through one OpenAI-compatible client (`muse/model.py`), configured by
`MUSE_BASE_URL`, `MUSE_API_KEY` and `MUSE_MODEL`.

## Out of scope

Team trees beyond the two agents, routing, helper marketplaces, phone apps, journals, coding agents other
than Claude Code and Muse Code, image models.

## Demo (3 minutes)

You say a preference aloud → it becomes a generalized Compass line, you confirm → a new session in that
repo starts with the line already loaded → the agent makes the choice you would → the gate's chart: the
agent matches your decisions more often, and you are interrupted less.

## Open questions (resolve Oct 27)

- Muse Voice Transcribe: endpoint, audio format, latency.
- Muse Code as an MCP client: stdio or streamable-HTTP, and whether it puts server `instructions` in
  its prompt.
- Structured output support, for the decision model's typed answers.
