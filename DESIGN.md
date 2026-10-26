# Design guide

Entry for the Meta Global AI Developer Hackathon (building Oct 27 – Nov 4, 2026), theme **Build for
Your World**, indie track. Everything in this repository was written during the event.

## The problem

A person running coding agents spends the day answering the same approval prompts and repeating the
same rules. Rules typed into one session are lost in the next.

## The product, in one loop

1. **Say a rule once.** You speak ("from now on, you can run the test suite without asking").
   Muse Voice Transcribe turns it into text; Muse Spark decides whether it is a standing rule and
   words it as one line; you confirm; it becomes a **Compass** line.
2. **The agent stops asking.** When the agent is about to ask you, a permission hook checks the call
   against the Compass. A matching line lets it through. No match: the agent's own permission
   prompt decides, unchanged.
3. **Prove it.** A gate runs the same tasks with and without the change (here: with and without the
   rule) and reports interruptions per task, pass rate and seconds. You decide from the numbers.

## Rules the design keeps

- **Only you write the Compass.** A line is saved only after your confirmation at a terminal; an
  agent's words never become a rule.
- **No checks of our own.** The hook can only remove a prompt the Compass covers; it never adds one.
  When it cannot decide (no model, no answer), it stays silent and the agent's permissions apply.
- **Keep it simple.** The Compass is one JSON-lines file: the index and the content. No logs, no
  server, no database.
- **A change is reviewed, then benchmarked, then you decide.** Passing tests is not a review.

## The Compass host

Ingestion and injection live in our `muse serve` client. It is the only client of the session, so
input it sends is the owner's. Per owner turn, Muse Spark makes three voted decisions: **add** (a new
rule?), **update** (refines or contradicts line X?) and **inject** (which lines matter now?). Add and
update are proposals the owner confirms; inject prepends the lines to the turn as data, not commands.
Approvals and check-ins are answered from the Compass only on a match; an unknown request kind is never
auto-approved, and a protected write allows allow-once only. Claude Code gets the same logic through
hooks (UserPromptSubmit context, PermissionRequest). Precision and recall of the three decisions are
measured in the gate.

## Meta models

| Job | Model |
|-----|-------|
| Speech to text | Muse Voice Transcribe (Meta Model API) |
| Is this a rule? Word it. Does a line allow this call? | Muse Spark (Meta Model API), or Muse Spark Open / Muse Glimmer (open weights) |
| Coding agent, reviewer, visible work | Muse Code 1.4.13, driven through `muse serve` (MSP session host); the Compass answers its approval requests on the wire |
| Second coding agent | Claude Code, approvals through its permission hook; the two agents chat with each other through the harness |

All model calls go through one OpenAI-compatible client (`muse/model.py`), configured by
`MUSE_BASE_URL`, `MUSE_API_KEY` and `MUSE_MODEL`.

## Out of scope

Team trees beyond the two agents, routing, helper marketplaces, phone apps, journals, coding agents other
than Claude Code and Muse Code, image models.

## Demo (3 minutes)

The agent stalls on an approval prompt → you say the rule → it appears as a Compass line, you
confirm → the task re-runs with no prompts → the gate's chart: interruptions down, pass rate held.

## Open questions (resolve Oct 27)

- Muse Voice Transcribe: endpoint, audio format, latency.
- Muse Code: hooks and headless mode exist (plugins, `muse exec`, `muse serve`); open: which MSP events
  carry approval requests and tool calls.
- Structured output support, for the rule and match answers.
