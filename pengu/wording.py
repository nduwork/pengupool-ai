"""M5 wording: one routine that turns words into Compass lines and lessons.

Muse Spark, off the turn's hot path: only called when the owner states or
changes a preference (ingest) or when a task ends (learned). It generalizes
one case into the rule behind it, never the other way round.

Inputs are scrubbed before the model sees them; the caller scrubs the
result again before storing (defense in depth, and the mock path).
"""

from .judge import redact
from .model import chat

def generalize_compass(words, model=None):
    """One standing rule behind what was said, or '' if there is none.

    Both the input and the result are scrubbed: the model echoes what it
    was given, and callers scrub once more before storing."""
    out = chat([{"role": "system", "content": (
        "Turn what was said into the single standing rule behind it: one short "
        "imperative line a coding agent can follow every time (\"Always run the "
        "test suite before committing\"). Generalize the case, keep the intent. "
        "If it states no standing rule, answer exactly: none")},
        {"role": "user", "content": redact(words)}], model=model)
    line = (out or "").strip()
    if len(line) >= 2 and line[0] == line[-1] and line[0] in "\"'":
        line = line[1:-1].strip()
    if not line or line.rstrip(". ").lower() in ("none", "no standing rule"):
        return ""
    return redact(line.split("\n")[0].strip())


def generalize_lesson(summary, model=None):
    """A general lesson (<=5 lines) from an agent's compacted task summary."""
    out = chat([{"role": "system", "content": (
        "Turn this task summary into the general lesson behind it: what to do "
        "or avoid next time a similar task comes up. At most 5 lines, concrete, "
        "no task-specific names or secrets.")},
        {"role": "user", "content": redact(summary)}], model=model)
    lines = [redact((l or "").strip()) for l in (out or "").splitlines() if l.strip()]
    return "\n".join(lines[:5])
