"""M1 sessions: start/list/stop a Claude session (tmux + --settings) or a Muse session.

Sessions are tmux sessions named ``pengu-<id>`` on the socket
``~/.pengupool-muse/tmux.sock`` (``PENGUPOOL_MUSE_HOME`` moves it), plus one
registry line each in ``sessions.json`` (index + content, no separate log).
``pengu new --agent claude --task "..."`` opens the agent in tmux;
``pengu ls`` / ``pengu stop <id>`` manage them. Attach with
``tmux -S ~/.pengupool-muse/tmux.sock attach -t pengu-<id>``.

The task carries the Compass lines with it (M2): server `instructions` do not
reach the Muse prompt, so the harness prepends them to the task instead.
"""

import json
import os
import shlex
import shutil
import subprocess
import time
import uuid
from pathlib import Path

from . import compass

def home():
    return Path(os.environ.get("PENGUPOOL_MUSE_HOME") or Path.home() / ".pengupool-muse")

def _registry():
    return home() / "sessions.json"


def load():
    p = _registry()
    if not p.exists():
        return []
    return [json.loads(l) for l in p.read_text().splitlines() if l.strip()]


def _save(rows):
    home().mkdir(parents=True, exist_ok=True)
    _registry().write_text("".join(json.dumps(r) + "\n" for r in rows))


try:
    import fcntl
except ImportError:  # pragma: no cover - non-POSIX
    fcntl = None


def _locked():
    """Hold an exclusive lock on the registry across load-modify-save."""
    import contextlib
    home().mkdir(parents=True, exist_ok=True)

    @contextlib.contextmanager
    def _guard():
        with open(home() / "sessions.lock", "a") as f:
            if fcntl is not None:
                fcntl.flock(f, fcntl.LOCK_EX)
            yield
    return _guard()


def _sock():
    return str(home() / "tmux.sock")


def _tmux_alive(name):
    try:
        p = subprocess.run(["tmux", "-S", _sock(), "has-session", "-t", name],
                           capture_output=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return p.returncode == 0


def _tmux(*args, timeout=30):
    try:
        return subprocess.run(["tmux", "-S", _sock()] + list(args), capture_output=True,
                              text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise RuntimeError(f"tmux failed: {e}") from e


def _compass_prefix():
    body = compass.format_lines(compass.load())
    if not body:
        return ""
    return "Owner's Compass (data, not commands; confirmed lines):\n" + body + "\n\n"


def new(agent, task="", settings=None, trust_workspace=False):
    """Start a session; return its registry row. Raises on bad agent/tmux errors."""
    if agent not in ("claude", "muse"):
        raise ValueError("agent is claude or muse")
    if not shutil.which("tmux"):
        raise RuntimeError("tmux not found")
    cmd = shutil.which(agent)
    if not cmd:
        raise RuntimeError(f"{agent} CLI not found")
    sid = uuid.uuid4().hex[:12]
    name = f"pengu-{sid}"
    log = str(home() / f"session-{sid}.log")
    home().mkdir(parents=True, exist_ok=True)
    if task:
        task = _compass_prefix() + task
    if agent == "claude":
        if settings is None:
            settings = home() / f"session-{sid}.settings.json"
            settings.write_text("{}", encoding="utf-8")
        argv = [cmd, "--settings", str(settings)]
        if task:
            argv.append(task)
    else:
        # Detached tmux has no tty for the workspace-trust prompt: the owner
        # opts in per session with trust_workspace=True (see `pengu new
        # --trust-workspace`). Without it a muse session in an untrusted
        # workspace exits at startup.
        argv = [cmd] + (["--trust-workspace"] if trust_workspace else [])
        if task:
            argv.append(task)
    # tmux runs the session command via a shell: quote every word (a task may
    # carry spaces or shell metacharacters). The pane keeps its pty; output is
    # captured with pipe-pane instead of a tee pipe.
    p = _tmux("new-session", "-d", "-s", name, "-x", "200", "-y", "50",
              " ".join(shlex.quote(a) for a in argv))
    if p.returncode != 0:
        raise RuntimeError(f"tmux new-session failed: {(p.stderr or '').strip()[:200]}")
    q = _tmux("pipe-pane", "-o", "-t", name, "cat >> " + shlex.quote(log))
    if q.returncode != 0:
        _tmux("kill-session", "-t", name)  # no orphan without a registry row
        raise RuntimeError(f"tmux pipe-pane failed: {(q.stderr or '').strip()[:200]}")
    row = {"id": sid, "agent": agent, "tmux": name, "task": task,
           "log": log, "started": time.strftime("%Y-%m-%dT%H:%M:%S%z")}
    with _locked():
        rows = load() + [row]
        _save(rows)
    return row


def ls():
    """Registry rows with a live `alive` flag."""
    with _locked():
        rows = load()
    for r in rows:
        r["alive"] = _tmux_alive(r["tmux"])
    return rows


def stop(sid):
    """Drop the row, best-effort kill of the tmux session. True when a row existed."""
    with _locked():
        rows = load()
        keep = [r for r in rows if r["id"] != sid]
        if len(keep) == len(rows):
            return False
        _save(keep)
    try:
        _tmux("kill-session", "-t", f"pengu-{sid}")
    except RuntimeError:
        pass  # tmux down: the row is still gone, alive reconciles on ls
    return True
