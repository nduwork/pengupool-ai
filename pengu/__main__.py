"""pengupool-muse command line.

    python3 -m pengu rule "from now on, you can run the tests without asking"
    python3 -m pengu list
    python3 -m pengu forget ID
    python3 -m pengu check '{"tool": "Bash", "input": {"command": "pytest"}}'
    git diff HEAD | python3 -m pengu review
    python3 -m pengu review HEAD~1
"""

import argparse
import json
import subprocess
import sys

from . import compass, review, rules
from .model import ModelError


def confirm(question):
    # Only the owner, at a real terminal, can save a rule; an agent's pipe cannot answer.
    # ponytail: an agent that drives a pty could; a hook guard on `pengu rule` comes with the hook.
    if not sys.stdin.isatty():
        sys.exit("a rule is saved only after you confirm it at a terminal")
    return input(question + " [y/N] ").strip().lower() == "y"


def _git_out(argv):
    try:
        p = subprocess.run(argv, capture_output=True, text=True,
                           encoding="utf-8", errors="replace")
    except OSError as e:
        sys.exit(f"git failed: {e}")
    if p.returncode != 0:
        sys.exit(f"git failed: {(p.stderr or '').strip() or ' '.join(argv)}")
    return p.stdout


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
        elif cmd == "review":
            ap = argparse.ArgumentParser(prog="pengu review")
            ap.add_argument("rev", nargs="?", default=None,
                            help="committed rev to review via git show (default: stdin, else worktree vs HEAD)")
            ns = ap.parse_args(args[1:])
            if ns.rev:
                diff = _git_out(["git", "show", ns.rev, "--"])
            elif not sys.stdin.isatty():
                diff = sys.stdin.read()
            else:
                diff = _git_out(["git", "diff", "HEAD", "--"])
            if not diff.strip():
                sys.exit("no diff to review")
            print(review.review_diff(diff))
        else:
            sys.exit(__doc__)
    except (ModelError, RuntimeError, ValueError) as e:
        sys.exit(f"pengu: {e}")


if __name__ == "__main__":
    main()
