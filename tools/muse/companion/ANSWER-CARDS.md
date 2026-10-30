# Answer cards — design (owner-reviewed Oct 30, 2026)

The UI for answering an agent's question from the owner's phone. Two
surfaces, each with one job: the **context card** renders nicely, the
**choice card** takes the tap. Early design mocks live in
`out/design-*.html`; the flow below is the settled one.

## Surfaces

1. **Context card** (HTML, display only) — asking agent + team/repo,
   PenguPool's red "Needs your input" chip, the question as title, the
   body in the agent's words when there is one, and a Context block:
   what the decision affects (from the ask description; until agent
   asks land, composed from the ask card + the session screen). It
   never carries options or buttons.
2. **Choice card** (native list widget) — shown only when the ask
   carries options, directly under the context card, and it contains
   ONLY the choices: one tappable row per option (option text + its
   one-line consequence), ending with **"My own answer…"** as a regular
   option in the same list (owner correction, Oct 30). No title or
   context of its own — the context card above it carries those. The
   owner picked this native card as-is after seeing its render limits
   (plain text rows; no colors, chips or selected states) and the
   richer alternatives.
3. **Open question** — context card only, NO choice card after it. The
   user answers in the conversation.

## Filing rules (owner, Oct 30)

- **A choice files immediately.** Tapping a provided option on the
  choice card IS the decision — Muse files it at once, no "send it"
  confirmation, and reports what was filed.
- **Confirmation is only for Muse's wording.** When the answer is
  free text that Muse helped formulate — wording that is not the
  user's own — Muse shows the exact text in the conversation and
  files only after the user's Send tap (or rewording). When the answer
  is the user's own words, it files directly.
- There is no Draft step and no Send button on any card; the
  conversation window is the drafting surface.

## The "My own answer" flow

Choosing it opens a conversation with Muse about the answer: the user
talks it through, Muse helps shape the wording. Once settled (with the
Send confirmation above when the wording is Muse's), **Muse operates
the API**: select the proper option for that ask and type in the
answer — the reply is filed through the ask's own answer path, never
typed into a terminal as a side channel. If the settled answer matches
one of the provided options, that option is the one selected in the
API; if it is genuinely custom, the ask's free-text/chat action
carries the text.

## Edge states

- Already answered → the context card re-renders in its closed state
  (green "Answered" chip, the filed answer shown, card inert).
- Question changed (payload hash moved) → the old card is stale; Muse
  re-issues context + choice cards with the new wording before any
  answer can be filed against it.

## Delivery paths

- Host asks (today): device API answer endpoint with ask_id +
  payload_hash (idempotent; 409 if the ask changed under you).
- Agent questions (piece 1/2): once the ask primitive lands, the same
  endpoint family. Terminal prompts in the interim still resolve via
  the say lane, with the same card flow on top.
