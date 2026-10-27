"""M5 ingest: owner words in, Compass proposals out. Evidence writes directly.

Classify the utterance; preference/restriction generalizes (Spark) into a
new-line proposal, or records evidence when it supports or contradicts an
existing line (support up, contradiction down, age discounts). Structural
changes — new lines, rewordings, retirements — only ever become proposals.
A correction that contradicts a line lowers its confidence and proposes a
rewording or retirement; a choice becomes a note for the owner. The owner
confirms every structural change; evidence is the only direct write.

Evidence keeps the owner's verbatim words: it is owner-local history, never
served to a model. Only generalized (and redacted) text leaves the store —
as new lines, rewordings, or task prefixes. Proposal bodies and their refs
quote the owner's words verbatim: they are shown to the owner for
confirmation, never to another model.
"""

import re

from . import compass, judge, owner, wording


def _overlap(a, b):
    toks = lambda s: set(re.findall(r"[A-Za-z][A-Za-z0-9_-]{2,}", s.lower()))
    return len(toks(a) & toks(b))


def _best_match(items, words, model, relation=None, limit=10):
    """Best (item, relation, confidence) by judge relate; None when nothing
    matches. relation=None accepts supports or contradicts; otherwise only
    that one. A word-overlap prefilter caps the judge to `limit` lines when
    the store is big; small stores go to the judge whole so paraphrases
    still match."""
    scored = sorted((( _overlap(words, it.get("rule", "")), it) for it in items),
                    key=lambda t: t[0], reverse=True)
    if len(scored) <= limit:
        cands = [it for _, it in scored]
    else:
        cands = [it for score, it in scored[:limit] if score > 0]
    best = None
    for item in cands:
        try:
            r = judge.relate(words, item.get("rule", ""), model=model)
        except ValueError:
            continue
        if r["relation"] == "unrelated":
            continue
        if relation is not None and r["relation"] != relation:
            continue
        if best is None or r["confidence"] > best[2]:
            best = (item, r["relation"], r["confidence"])
    return best


def _propose(words, kind, ref):
    code, out = owner.save_proposal(words, kind, ref)
    if code != 202:
        raise RuntimeError(out.get("error", "proposal failed"))
    return out.get("id")


def _after_contradict(existing, words, session_id, reword_text):
    """Record the contradiction, then propose retirement or rewording.
    reword_text None means generalization came back empty: propose a note.
    Returns (proposal_id, outcome, message) with outcome in
    {retired, reworded, noted, vanished}."""
    updated = compass.update_evidence(existing["id"], "contradicts", words, session_id)
    if updated is None:
        pid = _propose(f"Line vanished mid-ingest ({existing['id']}): {existing['rule']}",
                       "note", {"line_id": existing["id"], "reason": words})
        return pid, "vanished", "proposed note (line vanished)"
    conf = updated["confidence"]
    if conf < 0.3:
        pid = _propose(f"Retire line ({existing['id']}): {existing['rule']} — contradicted",
                       "retire", {"line_id": existing["id"], "reason": words})
        return pid, "retired", "proposed retirement (confidence %.2f)" % conf
    if not reword_text:
        pid = _propose(
            f"Contradiction needs owner wording ({existing['id']}): {existing['rule']}",
            "note", {"line_id": existing["id"], "reason": words})
        return pid, "noted", "proposed note (owner words the rewording)"
    pid = _propose(f"Reword line ({existing['id']}): {existing['rule']} — see: {words}",
                   "reword", {"line_id": existing["id"], "text": reword_text, "reason": words})
    return pid, "reworded", "proposed rewording (confidence %.2f)" % conf


def ingest(words, session_id, model=None):
    """Run the wording routine; returns (proposal_id, description)."""
    if not isinstance(words, str) or not words.strip():
        raise ValueError("empty words")
    kind = judge.classify(words, model=model)["kind"]
    if kind in ("preference", "restriction"):
        match = _best_match(compass.load(), words, model)
        if match is None:
            line = judge.redact(wording.generalize_compass(words, model=model))
            if not line:
                return None, "no standing rule in that"
            pid = _propose(f"New Compass line: {line}", "new-line", {"text": line})
            return pid, f"proposed new line: {line}"
        existing, rel, _ = match
        if rel == "supports":
            if compass.update_evidence(existing["id"], "supports", words, session_id) is None:
                pid = _propose(f"Line vanished mid-ingest ({existing['id']})",
                               "note", {"line_id": existing["id"], "reason": words})
                return pid, "proposed note (line vanished)"
            return None, f"supports line {existing['id']}: evidence recorded"
        line = judge.redact(wording.generalize_compass(words, model=model))
        pid, _, msg = _after_contradict(existing, words, session_id, line or None)
        return pid, msg
    if kind == "correction":
        match = _best_match(compass.load(), words, model, relation="contradicts")
        if match is None:
            pid = _propose(f"Correction with no matching line: {words}", "note", None)
            return pid, "proposed note (no matching line)"
        existing, _, _ = match
        text = judge.redact(wording.generalize_compass(words, model=model))
        if text == existing["rule"]:
            text = ""
        pid, outcome, msg = _after_contradict(existing, words, session_id, text or None)
        if outcome == "reworded":
            return pid, "proposed rewording after correction"
        return pid, msg
    pid = _propose(f"Decision for the owner: {words}", "note", None)
    return pid, "proposed note (choice/fyi: owner decides)"
