---
name: {{task}}
description: |
  Use when asked to {{description}}. TODO: say what it does and list the concrete asks that should load it; not where its content came from (a credit a license requires goes in ## Credits at the end).
triggers:
  - {{task}}
---

# {{task}}

TODO: the method. Keep it to the steps an agent cannot guess.

## Every task

1. Read `MEMORY.md` (repo root) if present and apply it.
2. Do the task. TODO: steps.
3. Log it by subject (`AGENTS.md` rule 5): find the subject in `logs/INDEX.md`, append a dated entry to its
   `logs/<subject>.md` and update its *Last updated*; start a new log and row only for a new subject.
4. Run the memory ritual (`docs/memory.md`) and reply with the outcome and the log path.
