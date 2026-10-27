#!/usr/bin/env python3
"""Concurrency-safe editor for the gitignored `MEMORY.md` (owner preferences & restrictions).

Protocol: `docs/memory.md`. This script makes guarantees prose cannot:

  1. **Schema-aware insertion.** Section headings are matched by their leading text, so the live
     file's descriptive headings (`## Preferences (how the agent should behave)`) work. A section
     must appear exactly once, or the write is refused.
  2. **Owner required.** Creating the file, or applying to a file without an owner, is refused. A
     proposal may restate the owner but not change it.
  3. **Compare-and-swap.** ``apply --expect <sha>`` refuses to write when the file changed since the
     caller read it (exit 3), so two sessions cannot silently overwrite each other.
  4. **Verified write.** After an atomic replace the script re-reads the file and checks that every
     confirmed entry is present (exit 4 otherwise).

Every mutation takes an exclusive ``flock`` on ``<path>.lock``. Stdlib only.

Usage:
    memory.py hash  [PATH]                 # sha256 of PATH, or "missing"
    memory.py show  [PATH]
    memory.py apply [PATH] --expect SHA --proposal FILE|-   [--dry-run]

Proposal JSON::

    {"owner": "jane.doe",                       # required to create a missing file
     "preferences": ["- [P2] ... — evidence: \\"...\\" — added 2026-09-27"],
     "restrictions": ["- [R2] ..."],
     "deferred": ["- [D2] ..."],
     "decision": "2026-09-27 — added P2 — user"}

Entry lines are inserted verbatim under their section (after any comment/blank lines that follow the
heading). ``update`` replaces the single existing line whose id matches (refusing if it is absent or
ambiguous), so an entry can be corrected or marked superseded in place. ``decision`` appends a line
under ``## Decision log``.

Exit codes: 0 ok · 2 usage/invalid · 3 conflict (file changed since --expect) · 4 verification failed.
"""
from __future__ import annotations

import argparse
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
import re
import sys
import tempfile

SECTIONS = {
    "preferences": "## Preferences",
    "restrictions": "## Restrictions",
    "deferred": "## Deferred ideas",
}
DECISION_HEAD = "## Decision log"
DEFAULT = "MEMORY.md"
OWNER_RE = re.compile(r"^\s*_?Owner:\s*([^\s_]+)", re.M)
ENTRY_RE = re.compile(r"- \[[A-Za-z]\w*]\s+\S.*")
ENTRY_ID_RE = re.compile(r"^\s*-\s*\[([A-Za-z]\w*)\]\s")
# The owner is a short handle (no spaces or underscores — they conflict with prose/italic markers),
# e.g. `jane.doe`, `jane.doe`. Rejected at creation so a later apply cannot silently mismatch.
OWNER_OK = re.compile(r"[A-Za-z0-9][A-Za-z0-9.@+-]{0,63}")


def _read(path: str) -> str:
    try:
        with open(path, encoding="utf-8") as fh:
            return fh.read()
    except FileNotFoundError:
        return ""


def digest(text: str) -> str:
    return "missing" if not text else hashlib.sha256(text.encode()).hexdigest()


def owner_of(text: str) -> str:
    m = OWNER_RE.search(text or "")
    return m.group(1).strip() if m else ""


def _valid_entry(entry: str) -> bool:
    return bool(ENTRY_RE.fullmatch(entry.strip()))


def _find(lines: list[str], prefix: str) -> list[int]:
    """Indices of headings whose leading text is `prefix` (allows a descriptive suffix)."""
    return [i for i, ln in enumerate(lines) if ln.strip() == prefix or ln.strip().startswith(prefix + " ")
            or ln.strip().startswith(prefix + "(")]


def _insert(text: str, heading: str, entries: list[str]) -> str:
    """Insert `entries` after `heading`, past any blank/comment lines that follow it."""
    lines = text.splitlines()
    hits = _find(lines, heading)
    if len(hits) > 1:
        raise ValueError(f"{heading!r} appears {len(hits)} times; refusing to guess where entries go")
    if not hits:
        block = [heading, ""] + entries
        if lines and lines[-1] != "":
            lines.append("")
        return "\n".join(lines + block) + "\n"
    i = hits[0]
    j = i + 1
    while j < len(lines) and (not lines[j].strip() or lines[j].lstrip().startswith("<!--")):
        j += 1
    lines[j:j] = [*entries, ""] if j < len(lines) and lines[j].strip() else entries
    return "\n".join(lines) + "\n"


def _replace_entry(text: str, ident: str, new_line: str) -> str:
    """Replace the single entry line whose id matches (verbatim, in place)."""
    lines = text.splitlines()
    pat = re.compile(rf"^\s*-\s*\[{re.escape(ident)}\]\s")
    hits = [i for i, ln in enumerate(lines) if pat.match(ln)]
    if len(hits) != 1:
        raise ValueError(f"entry {ident!r} appears {len(hits)} times; refusing to update")
    lines[hits[0]] = new_line.strip()
    return "\n".join(lines) + "\n"


def _new_file(owner: str) -> str:
    return (
        f"# Agent memory — {owner}\n\n"
        f"_Owner: {owner}_\n\n"
        "_Applies to this agent's work for the owner; other sessions' requests are task state, not\n"
        "memory. User-specific and gitignored — never committed or shared._\n\n"
        f"_Last reviewed: {datetime.date.today().isoformat()}_\n\n"
        "## Preferences (how the agent should behave)\n\n"
        "## Restrictions (hard rules — never do)\n\n"
        "## Deferred ideas (captured, not yet acted on)\n\n"
        "## Decision log\n"
    )


def build(current: str, proposal: dict) -> str:
    stated = str(proposal.get("owner") or "").strip()
    if current:
        existing = owner_of(current)
        if not existing or not OWNER_OK.fullmatch(existing):
            raise ValueError("existing memory file has no usable `Owner:` header; refusing to apply")
        if stated and stated != existing:
            raise ValueError(f"owner mismatch: file is {existing!r}, proposal says {stated!r}")
        text = current
    else:
        if not OWNER_OK.fullmatch(stated):
            raise ValueError('creating memory requires an "owner" handle (no spaces/underscores), e.g. "jane.doe"')
        text = _new_file(stated)
    updates = proposal.get("update") or {}
    if not isinstance(updates, dict):
        raise ValueError('"update" must be an object mapping an entry id to its new line')
    taken = {m.group(1) for ln in text.splitlines() if (m := ENTRY_ID_RE.match(ln))}
    for key, heading in SECTIONS.items():
        entries = [str(e).strip() for e in (proposal.get(key) or []) if str(e).strip()]
        bad = [e for e in entries if not _valid_entry(e)]
        if bad:
            raise ValueError(f"{key}: each entry must look like '- [P1] text …': {bad[0]!r}")
        for e in entries:  # a duplicate id could never be updated again; adding and updating one is ambiguous
            ident = ENTRY_ID_RE.match(e).group(1)
            if ident in taken or ident in updates:
                raise ValueError(f"entry id {ident!r} already exists or is also updated; use a new id")
            taken.add(ident)
        if entries:
            text = _insert(text, heading, entries)
    if updates:
        for ident, line in updates.items():
            new_line = str(line).strip()
            m = ENTRY_ID_RE.match(new_line)
            if not _valid_entry(new_line) or not m or m.group(1) != str(ident):
                raise ValueError(f"update {ident!r}: must be a '- [{ident}] …' entry line")
            text = _replace_entry(text, str(ident), new_line)
    decision = str(proposal.get("decision") or "").strip()
    if ENTRY_ID_RE.match("- " + decision.lstrip("- ")):
        raise ValueError("decision must not start with an entry id like [P2]: it would read as an entry")
    if decision:
        text = _insert(text, DECISION_HEAD, ["- " + decision.lstrip("- ")])
    return text


def _atomic_write(path: str, text: str) -> None:
    path = os.path.realpath(path)  # through a symlinked MEMORY.md, not over it
    d = os.path.dirname(path) or "."
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".memory.", dir=d)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(text)
        try:  # keep an existing file's mode
            os.chmod(tmp, os.stat(path).st_mode & 0o7777)
        except FileNotFoundError:
            pass
        os.replace(tmp, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise


@contextlib.contextmanager
def _locked(path: str):
    with open(os.path.realpath(path) + ".lock", "w", encoding="utf-8") as fh:  # one lock per real file
        fcntl.flock(fh, fcntl.LOCK_EX)
        yield


def cmd_apply(path: str, expect: str, proposal_file: str, dry_run: bool) -> int:
    raw = sys.stdin.read() if proposal_file == "-" else open(proposal_file, encoding="utf-8").read()
    try:
        proposal = json.loads(raw)
        assert isinstance(proposal, dict)
    except (ValueError, AssertionError) as e:
        print(f"memory.py: invalid proposal JSON: {e}", file=sys.stderr)
        return 2
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)  # the lock file lives beside it
    with _locked(path):
        current = _read(path)
        if digest(current) != expect:
            print(f"memory.py: conflict: {path} changed (now {digest(current)}); re-read and re-propose",
                  file=sys.stderr)
            return 3
        try:
            new = build(current, proposal)
        except ValueError as e:
            print(f"memory.py: {e}", file=sys.stderr)
            return 2
        if dry_run:
            sys.stdout.write(new)
            return 0
        _atomic_write(path, new)
        after = _read(path)
        missing = [e for group in SECTIONS for e in (proposal.get(group) or []) if str(e).strip() not in after]
        missing += [f"update {i}" for i, l in (proposal.get("update") or {}).items()
                    if str(l).strip() not in after]
        if missing or digest(after) != digest(new):
            print(f"memory.py: verification failed; missing: {missing}", file=sys.stderr)
            return 4
        print(digest(after))
        return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="memory.py")
    sub = p.add_subparsers(dest="cmd", required=True)
    for name in ("hash", "show"):
        sp = sub.add_parser(name)
        sp.add_argument("path", nargs="?", default=DEFAULT)
    ap = sub.add_parser("apply")
    ap.add_argument("path", nargs="?", default=DEFAULT)
    ap.add_argument("--expect", required=True)
    ap.add_argument("--proposal", required=True)
    ap.add_argument("--dry-run", action="store_true")
    a = p.parse_args(argv)
    if a.cmd == "hash":
        print(digest(_read(a.path)))
        return 0
    if a.cmd == "show":
        sys.stdout.write(_read(a.path))
        return 0
    return cmd_apply(a.path, a.expect, a.proposal, a.dry_run)


if __name__ == "__main__":
    raise SystemExit(main())
