"""M6 inject: choose lines by context and confidence. Plain code, no model call.

Score = word overlap between the context (repo path, task text, topic) and
the rule, times confidence. Lines below 0.3 confidence (contradicted) never
inject; a non-empty context with no usable terms matches nothing, otherwise
zero-overlap lines are dropped and the rest rank by score. Caps: k lines,
character budget on the joined text.
"""

import re

MIN_CONFIDENCE = 0.3

_STOP = frozenset("""a an and are as at be but by did do does for from had has have
in is it its of on or that the this to was with you your""".split())

HELPER_HINT = ("When you get a job, first call compass_context for the topic "
               "and experience_for for your helper name: the Compass lines and "
               "the lessons of earlier tasks.")


def confidence(line):
    try:
        return float(line.get("confidence", 1.0))
    except (TypeError, ValueError):
        return 1.0


def _terms(s):
    out = set()
    for w in re.findall(r"[A-Za-z][A-Za-z0-9_-]{2,}", str(s or "").lower()):
        if w in _STOP or len(w) < 4:
            continue  # function words and stubs carry no topic
        if w.endswith("s") and not w.endswith("ss") and len(w) > 4:
            w = w[:-1]  # crude plurals, applied to both sides alike
        out.add(w)
    return out


def cap(items, k, budget):
    """First k items within the character budget (first one always kept)."""
    out, size = [], 0
    for line in items[:max(0, k)]:
        size += len(str(line.get("rule", "")))
        if size > budget and out:
            break
        out.append(line)
    return out


def select(lines, context, k=8, budget=2000):
    """Relevant lines first, capped. Returns the chosen line dicts.

    A non-empty context with no usable terms matches nothing; an empty
    context ranks everything above the floor by confidence."""
    ctx = _terms(context)
    if isinstance(context, str) and context.strip() and not ctx:
        return []
    scored = []
    for line in lines:
        conf = confidence(line)
        if conf < MIN_CONFIDENCE:
            continue
        overlap = len(ctx & _terms(line.get("rule", ""))) if ctx else 0
        if ctx and overlap == 0:
            continue
        scored.append((overlap * conf, conf, line))
    scored.sort(key=lambda t: (-t[0], -t[1]))
    return cap([line for _, _, line in scored], k, budget)
