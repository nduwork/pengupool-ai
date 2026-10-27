"""pengupool-muse command line.

    python3 -m pengu rule "from now on, you can run the tests without asking"
    python3 -m pengu list
    python3 -m pengu forget ID
    python3 -m pengu check '{"tool": "Bash", "input": {"command": "pytest"}}'
    git diff HEAD | python3 -m pengu review
    python3 -m pengu review HEAD~1
    python3 -m pengu new --agent muse --task "hello" --trust-workspace
    python3 -m pengu ls
    python3 -m pengu stop ID
    python3 -m pengu mcp   # serve the Compass MCP server on stdio
    python3 -m pengu send --from <id> --to <id|owner> --text "..."   # chat along the tree
    python3 -m pengu inbox <id|owner>
    python3 -m pengu ask --session <id> --question "..." --option A --option B
    python3 -m pengu owner-serve   # the Muse-app agent's HTTP window (needs PENGUPOOL_OWNER_TOKEN)
    python3 -m pengu proposals   # proposals the owner has not confirmed yet
"""

import argparse
import json
import subprocess
import sys

from . import chat, compass, mcp, owner, review, rules, session
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
        elif cmd == "new":
            ap = argparse.ArgumentParser(prog="pengu new")
            ap.add_argument("--agent", required=True, choices=["claude", "muse"])
            ap.add_argument("--task", default="")
            ap.add_argument("--trust-workspace", action="store_true",
                            help="muse only: pass --trust-workspace (skips the trust prompt)")
            ns = ap.parse_args(args[1:])
            if ns.trust_workspace and ns.agent != "muse":
                ap.error("--trust-workspace is muse only")
            row = session.new(ns.agent, ns.task, trust_workspace=ns.trust_workspace)
            print(f"{row['id']}  {row['agent']}  tmux:{row['tmux']}")
        elif cmd == "ls":
            for r in session.ls():
                print(f"{r['id']}  {r['agent']}  {'alive' if r['alive'] else 'dead'}  {r['task'][:60]}")
        elif cmd == "stop" and len(args) == 2:
            print("stopped" if session.stop(args[1]) else "no such session")
        elif cmd == "mcp":
            mcp.serve_stdio()
        elif cmd == "send":
            ap = argparse.ArgumentParser(prog="pengu send")
            ap.add_argument("--from", dest="from_sid", required=True)
            ap.add_argument("--to", dest="to_sid", required=True)
            ap.add_argument("--text", required=True)
            ns = ap.parse_args(args[1:])
            e = chat.send(ns.from_sid, ns.to_sid, ns.text)
            print(f"{e['id']}  {e['from']} -> {e['to']}")
        elif cmd == "inbox" and len(args) == 2:
            for e in chat.inbox(args[1]):
                print(f"{e['id']}  from {e['from']}: {e['text']}")
        elif cmd == "ask":
            ap = argparse.ArgumentParser(prog="pengu ask")
            ap.add_argument("--session", required=True)
            ap.add_argument("--question", required=True)
            ap.add_argument("--option", action="append", required=True)
            ap.add_argument("--expires", type=int, default=300)
            ns = ap.parse_args(args[1:])
            q = owner.create_question(ns.session, ns.question, ns.option, ns.expires)
            print(f"question {q['id']} waiting for owner...")
            opt, err = owner.wait_answer(q["id"], ns.expires)
            if err == "timeout":
                sys.exit(f"still waiting: question {q['id']} is pending (owner has not answered)")
            if err:
                sys.exit(f"no answer: {err}")
            print(opt)
        elif cmd == "owner-serve":
            ap = argparse.ArgumentParser(prog="pengu owner-serve")
            ap.add_argument("--bind", default="127.0.0.1")
            ap.add_argument("--port", type=int, default=8471)
            ns = ap.parse_args(args[1:])
            owner.main_serve(ns.bind, ns.port)
        elif cmd == "proposals":
            for p in owner.list_proposals():
                print(f"{p['at']:.0f}  {p['words']}  ({p['status']})")
        else:
            sys.exit(__doc__)
    except (ModelError, RuntimeError, ValueError, OSError) as e:
        sys.exit(f"pengu: {e}")


if __name__ == "__main__":
    main()
