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

## Meta models

| Job | Model |
|-----|-------|
| Speech to text | Muse Voice Transcribe (Meta Model API) |
| Is this a rule? Word it. Does a line allow this call? | Muse Spark (Meta Model API), or Muse Spark Open / Muse Glimmer (open weights) |
| The coding agent under test | Muse Code if it exposes hooks; otherwise an agent on Muse Spark Open |

All model calls go through one OpenAI-compatible client (`muse/model.py`), configured by
`MUSE_BASE_URL`, `MUSE_API_KEY` and `MUSE_MODEL`.

## Out of scope

Team trees and messaging, routing, helper marketplaces, phone apps, journals, more than one coding
agent, image models.

## Demo (3 minutes)

The agent stalls on an approval prompt → you say the rule → it appears as a Compass line, you
confirm → the task re-runs with no prompts → the gate's chart: interruptions down, pass rate held.

## Open questions (resolve Oct 27)

- Muse Voice Transcribe: endpoint, audio format, latency.
- Muse Code: does it have hooks (prompt, pre-tool-use, stop) and a headless mode? This decides the
  agent under test.
- Structured output support, for the rule and match answers.
