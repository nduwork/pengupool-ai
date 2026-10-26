"""The two questions Muse Spark answers: is this a standing rule, and does a Compass line allow this call."""

import json

from .model import ask_json

RULE_PROMPT = """You read what a developer said to their coding agent. Decide whether it sets a
standing rule: something the agent should do every time a kind of situation comes up ("from now on",
"always", "never", "whenever X, do Y", "you can X without asking"). A task about this one piece of
work is not a rule, even with a condition in it.
Answer with JSON only: {"is_rule": true|false, "rule": "<the rule as one short imperative line, or empty>"}"""

ALLOW_PROMPT = """A coding agent is about to make a tool call that would normally need the owner's
approval. Below are the owner's standing rules. Decide whether one rule clearly allows this exact
call. If none clearly does, answer null; never stretch a rule.
Answer with JSON only: {"allowed_by": "<rule id>" | null, "reason": "<one sentence>"}"""


def extract(said):
    """The rule in what the owner said, or None."""
    a = ask_json(RULE_PROMPT, said)
    rule = str(a.get("rule") or "").strip()
    return rule if a.get("is_rule") is True and rule else None


def allowed_by(call, lines):
    """The id of the Compass line that allows this tool call, or None."""
    if not lines:
        return None
    rules = "\n".join(f"{l['id']}: {l['rule']}" for l in lines)
    a = ask_json(ALLOW_PROMPT, f"Rules:\n{rules}\n\nTool call:\n{json.dumps(call)}")
    lid = a.get("allowed_by")
    return lid if lid in {l["id"] for l in lines} else None  # an id the model made up allows nothing
