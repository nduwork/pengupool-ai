# Plan: Compass & Experience curation (memory pile-up)

Revision 2, reviewed by pr-review-agent and checked against main at 17a0e890.

## Context

The first version of this doc proposed three stages to keep the Compass and Experience from piling up,
drawn from outside research and an audit of `internal/agentdb/compass.go`, `experience.go` and the
live store. The owner asked for it to be reviewed and turned into something that can be built. Each
claim below is checked against the code, and the plan that follows replaces the three stages.

## Review of the first version

| First-version claim / proposal | What the code says | Verdict |
|---|---|---|
| Stage 1.1: permission approvals never become principle lines | They are deliberate `perm:<way>:<tool>:<pattern>` lines (`learnPattern`, gates.go:847) that `autoAnswer` reads. `autoAnswer`, `floorWaiver`, Muse and search each read the DB on their own, so taking them out of the agent render breaks none of them. | Wrong fix, right symptom: keep storing them, stop rendering them. |
| "7 truncated lines" | The `compass_review` merge item only covers intact `perm:allow\|deny:Bash:` tags and skips exact/script patterns. Nothing shows the live 7 qualify. | Not proven handled. The owner checks `compass review`; anything it doesn't list gets deleted by hand. |
| Stage 1.2: no render budget | The cap is 32 KiB, applied after `p.lines("", 200)` takes the **200 newest** lines and then filters by Show (context.go:166). 200 newer permission lines would push out all prose, so agents get "No preferences yet" with no overflow notice. | True, and worse than #59 says. |
| Scope is free text in the wording | `condition` is its own column, but still free text. | Partly right. Structured scope is deferred. |
| Stage 2.4: search before write | `keepLine` writes first, then `mergeSame` merges with neighbours, and only when a decider is set. That is a merge after writing, not ADD/UPDATE/NOOP. `match` already sees a new signal that contradicts a line. | What's missing is a review of line pairs that contradict each other, and a way to resolve them. |
| Stage 3.6: consolidation pass | `compass_review.go` exists but only handles permission lines (broad, merge). | Extend it with a new item kind. |
| Experience dedup is exact only | Confirmed (helper + body hash, uid/tombstone). | True; deferred until counted. |

## Executable plan

Builder: **pengupool-ai-claude**. Reviewer: **pr-review-agent**. Only the owner merges (squash).
Each PR is rebased on the latest main before review, and its description is kept in line with the code.

### PR A: the agent render shows prose lines, picked before any limit, inside a strict budget

1. `internal/pool/context.go` `renderCompassCap`: take eligible lines **before** limiting. Call
   `p.lines("", 0)` (in `Lines`, limit 0 means no LIMIT), keep lines that are `v.shown()` and whose
   `Tag` does not start with `perm:` (same check as gates.go:331), then sort with `cmp.Or(guides first, Mu desc)`.
   `guides()` is a belief rank only, not owner trust; the render does not change trust.
   ponytail: replays every line's belief on each render. Add an SQL tag filter if the store grows into the thousands.
2. Budget semantics: `maxCompassContext = 8 << 10` bounds the **whole** section, including the header
   and the overflow line. Room for the "… more" line is reserved up front. Lines go in priority order,
   and the first line that does not fit ends the list with the overflow notice. A line is at most
   about 360 runes (`clipRunes` 240 + 120), so one line can never use up the budget.
3. Tests in `internal/pool/compass_test.go`:
   (a) a prose line plus a `perm:` line: only the prose line is rendered, and `autoAnswer` still allows `go test ./...`;
   (b) 250 newer `perm:` lines plus 1 older prose line: the prose line is still rendered (this case fails today);
   (c) budget: `len(renderCompassCap(n)) <= n` at small n, with the overflow notice present and guiding lines first.
4. Keep this doc in line with what PR A builds.
5. Built (v0.9.39): as above. A budget smaller than the header plus the overflow notice renders no
   compass section at all, rather than a header with nothing under it. After review: each rendered
   line is clipped to 360 runes (an owner's line may be 16 KiB), and a line that doesn't fit is
   skipped, not the end of the list; the "No preferences yet." section also respects the budget.

### PR B: conflict items in the Compass review, resolved as an explicit discard

The decision: when two lines contradict, the owner **discards** the losing line with the existing
`compassDelete` (DeleteLine). `MergeLines` is not used: it treats the two lines as the same
preference, so B's support would move to A as support, B's stated evidence would gain weight, A could
take B's condition, and sync would redirect B's later offline effects to A (agentdb.go:528–570,
sync.go:212–259). A discard loses B's history. Retiring B but keeping it for audit needs a schema and
sync change and is deferred (see the open item at the end).

0. Backend, `internal/pool/compass.go` `matchLines` (around :281): when the decision model finds a signal
   **only contradicts** lines (no line supports it), record those contradiction effects *and*, when
   `mk` is set, have the signal worded as its own new line (`askWording`, or `keepLine` when a check-in
   gave a `line`). The decision model can still decline to word a plain "no" with no replacement, as it does
   for one-off picks.
   Why (the owner's yellow/red example): today "red is my favorite" only counts against the yellow line, and
   no red line is made, so on day 2 agents see no color at all. With this step, red gets its own line,
   and step 1 shows yellow vs red as one conflict item.
   Test: a stated yellow line, then a said "red" signal that the fake decider marks as contradicting →
   the yellow line takes a p_con effect, and a new red line exists.
1. Backend, `internal/pool/compass_review.go`: new item kind `conflict`. Among shown non-`perm:` lines,
   take FTS neighbours (`p.candidates`) and ask the decider whether each pair contradicts under
   overlapping conditions (prompt shaped like `mergeSame`, compass.go:345). Group the contradicting
   pairs into connected components, so a three-line cluster is **one** item listing all its lines.
   With no decider, there are no conflict items. Cap the number of pairs asked per review.
2. `reviewLine` gains `Condition`, so the owner sees when each line applies.
3. Apply: the owner picks the line to keep, and the others are deleted with `DeleteLine`. Before deleting,
   check the item is still current: every listed line exists and nothing changed since the review
   (compare `updated_at`); otherwise return "review changed, refresh". Then refresh the review so
   overlapping items disappear.
4. Frontend (app review panel and its TS types): add the `conflict` kind, show each line's condition,
   offer one "keep this" choice per line, and send to the new conflict apply RPC, never to the
   permission-only `apply_merge`.
5. Tests: three contradicting lines become one item; applying "keep A" deletes B and C and leaves A's
   belief and condition unchanged (A's effects are compared before and after); a stale item (a line
   edited after the review) is refused; with no decider there are no items.

### Deferred (re-propose only with evidence)

- Retiring a line but keeping it for audit (`superseded_by`): a schema and sync-format change.
- Structured scope beyond `condition`; Experience near-duplicate checks and schema; use-telemetry and promotions.

## Verification

- `go test ./internal/pool/ ./internal/agentdb/` (new tests plus `TestCompassReview*`, `TestMatch*`
  and `TestContextOrder`), `go vet ./...`, and the frontend type check/build for PR B.
- After merge and deploy only, never on unmerged branches: an agent session's compass section has
  no "The owner allows Bash for …" lines and stays under 8 KiB; an earlier-allowed `go test` is still
  auto-answered; for PR B, `compass review` lists the sessions-on-quit cluster as one conflict item.
- pr-review-agent reviews each PR before it goes to the owner.

## Decisions

- Owner, after the yellow/red trace: a change of mind becomes its own line, and the conflict review
  surfaces it (PR B step 0).

## Open item: discard or retire

For conflicts, the plan discards the losing line with `DeleteLine`, unless the owner asks for
retirement with its history kept (`superseded_by`, a schema and sync change).
