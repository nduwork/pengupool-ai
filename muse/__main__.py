"""pengupool-muse command line.

    python3 -m muse rule "from now on, you can run the tests without asking"
    python3 -m muse list
    python3 -m muse forget ID
    python3 -m muse check '{"tool": "Bash", "input": {"command": "pytest"}}'
"""

import json
import sys

from . import compass, rules
from .model import ModelError


def confirm(question):
    # Only the owner, at a real terminal, can save a rule; an agent's pipe cannot answer.
    # ponytail: an agent that drives a pty could; a hook guard on `muse rule` comes with the hook.
    if not sys.stdin.isatty():
        sys.exit("a rule is saved only after you confirm it at a terminal")
    return input(question + " [y/N] ").strip().lower() == "y"


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    cmd = args[0] if args else ""
    try:
        if cmd == "rule" and len(args) == 2:
            rule = rules.extract(args[1])
            if not rule:
                print("No standing rule in that.")
            elif confirm(f"Save to the Compass: {rule}"):
                print("saved", compass.add(rule, "typed")["id"])
        elif cmd == "list":
            for l in compass.load():
                print(f"{l['id']}  {l['rule']}  ({l['source']}, {l['created']})")
        elif cmd == "forget" and len(args) == 2:
            print("forgotten" if compass.forget(args[1]) else "no such line")
        elif cmd == "check" and len(args) == 2:
            print(rules.allowed_by(json.loads(args[1]), compass.load()) or "no line allows it")
        else:
            sys.exit(__doc__)
    except ModelError as e:
        sys.exit(f"model: {e}")


if __name__ == "__main__":
    main()
