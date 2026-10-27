"""M4 judge: adapter for the configured decision model (Jev today, any local Ollama model)."""
import math
import os
import re

from .model import ask_json

KINDS = ("preference", "restriction", "choice", "correction", "none")

_TOKEN_KEY = r"(api[ _-]?key|api[ _-]?token|auth[ _-]?token)"
_PASS_KEY = r"(secret|password|passwd|pwd)"
_SECRET_RES = [
    re.compile(r"""(?i)["']?[A-Za-z0-9_]*""" + _TOKEN_KEY + r"""\b["']?\s*[:=]\s*("[^"]*"|'[^']*'|\S+)"""),
    re.compile(r"""(?i)["']?[A-Za-z0-9_]*""" + _PASS_KEY + r"""\b["']?\s*[:=]\s*("[^"]*"|'[^']*'|.+)"""),
    re.compile(r"""(?i)["']?[A-Za-z0-9_]*""" + _TOKEN_KEY + r"""\b["']?\s+(is|are)\s*[:=]?\s*("[^"]*"|'[^']*'|\S+)"""),
    re.compile(r"""(?i)["']?[A-Za-z0-9_]*""" + _PASS_KEY + r"""\b["']?\s+(is|are)\s*[:=]?\s*("[^"]*"|'[^']*'|.+)"""),
    re.compile(r"\bsk-[A-Za-z0-9_-]{8,}\b"),
    re.compile(r"\b(ghp_[A-Za-z0-9]+|gho_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+|AKIA[0-9A-Z]{16}|xox[bap]-[A-Za-z0-9-]+)\b"),
    re.compile(r"\b[A-Za-z0-9+/]{40,}={0,2}"),  # long blobs smell like tokens
]


def redact(text):
    """Scrub secrets before a model sees them. Machine tokens (`api key = X`)
    lose one token or a quoted span; password-like values (`password = ...`)
    lose the rest of the line, since passphrases have spaces. `is/are`
    phrasing redacts the same way. Errs toward redaction over prose."""
    for rx in _SECRET_RES:
        text = rx.sub("[REDACTED]", text)
    return text


def _resolve(model):
    return model or os.environ.get("PENGUPOOL_JUDGE_MODEL") or None


SIGNAL_PROMPT = """Read what was said to or by a coding agent. Classify it into exactly one kind:
- preference: a standing like ("prefer small commits", "from now on always X")
- restriction: a standing ban ("never touch X", "don't do Y without asking")
- choice: a one-off decision between options ("ship it?", "A or B?")
- correction: feedback that past behavior was wrong ("that broke X", "you should have Y")
- none: anything else (status, FYI, question with no decision)
Answer with JSON only: {"kind": "<one of the five>", "confidence": <0..1>}"""

RELATION_PROMPT = """A Compass line and a new utterance. Decide whether the utterance supports the
line (evidence it still holds), contradicts it (evidence it changed), or is unrelated.
Answer with JSON only: {"relation": "supports"|"contradicts"|"unrelated", "confidence": <0..1>}"""

RELEVANCE_PROMPT = """A helper's task and candidate lessons from earlier tasks. For each candidate,
say whether its lesson would help this task and how confident you are.
Answer with JSON only: {"ratings": [{"id": "<candidate id>", "relevant": true|false,
"confidence": <0..1>}]} (one rating per candidate, in the same order as listed;
repeat each id exactly)"""

def _check(d, key, allowed):
    if not isinstance(d, dict):
        raise ValueError(f"bad answer: {d!r}"[:200])
    v = d.get(key)
    if v not in allowed:
        raise ValueError(f"bad {key}: {v!r}")
    try:
        c = float(d.get("confidence", 0))
    except (TypeError, ValueError, OverflowError):
        raise ValueError(f"bad confidence: {d.get('confidence')!r}")
    if not math.isfinite(c):
        raise ValueError(f"bad confidence: {d.get('confidence')!r}")
    return v, max(0.0, min(1.0, c))


def classify(words, model=None):
    """Signal kind + confidence for an utterance. Raises on invalid answers."""
    kind, conf = _check(ask_json(SIGNAL_PROMPT, redact(words), model=_resolve(model)),
                        "kind", KINDS)
    return {"kind": kind, "confidence": conf}


def relate(words, line, model=None):
    """supports|contradicts|unrelated + confidence of words vs a Compass line."""
    rel, conf = _check(ask_json(RELATION_PROMPT, f"Line: {redact(line)}\nUtterance: {redact(words)}",
                                model=_resolve(model)),
                       "relation", ("supports", "contradicts", "unrelated"))
    return {"relation": rel, "confidence": conf}


def rate_relevance(task, candidates, model=None):
    """One batched call: [(id, relevant, confidence)], same order as candidates.

    Each rating needs a boolean relevance and a numeric confidence. Pairing is
    by echoed id when the model repeats the full id set (order-free); smaller
    models mangle ids, so a mismatched set falls back to position. Lessons
    are one-lined and capped: raw newlines would break positional matching
    and long lessons would bloat the prompt.
    """
    if not candidates:
        return []
    def _one_line(s, cap=300):
        return redact(" ".join(str(s).split())[:cap])
    listing = "\n".join(f"{c['id']}: {_one_line(c.get('title',''))} — {_one_line(c.get('lesson',''))}"
                        for c in candidates)
    a = ask_json(RELEVANCE_PROMPT,
                 f"Task: {redact(task)}\nCandidates:\n{listing}", model=_resolve(model))
    ratings = a.get("ratings") if isinstance(a, dict) else None
    if not isinstance(ratings, list) or len(ratings) != len(candidates):
        raise ValueError(f"bad ratings: {a!r}"[:200])
    for r in ratings:
        if not isinstance(r, dict) or not isinstance(r.get("relevant"), bool):
            raise ValueError(f"bad rating: {r!r}"[:200])
        try:
            conf = float(r.get("confidence", 0))
        except (TypeError, ValueError, OverflowError):
            raise ValueError(f"bad confidence: {r!r}"[:200])
        if not math.isfinite(conf):
            raise ValueError(f"bad confidence: {r!r}"[:200])
        r["confidence"] = max(0.0, min(1.0, conf))
    want = sorted(str(c["id"]) for c in candidates)
    got = sorted(str(r.get("id")) for r in ratings)
    if got == want:
        by_rating = {str(r.get("id")): r for r in ratings}
        pairs = [(c, by_rating[str(c["id"])]) for c in candidates]
    else:
        pairs = list(zip(candidates, ratings))  # mangled ids: trust the order
    return [{"id": c["id"], "relevant": r["relevant"], "confidence": r["confidence"]}
            for c, r in pairs]


def triage_route(words, model=None):
    """Route an owner-bound message: choice -> question, standing line -> proposal."""
    r = classify(words, model=model)
    route = {"choice": "question", "preference": "proposal", "restriction": "proposal",
             "correction": "proposal", "none": "fyi"}[r["kind"]]
    return {"route": route, **r}
