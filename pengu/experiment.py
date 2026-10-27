"""M8 experiment: the same tasks with and without Compass + experience.

About five tasks, each run twice: once bare, once with the Compass lines and
lessons in the prompt (the same text `pengu new` prepends). Per run:
(a) did the agent make the decision the owner would (judge.relate of the
transcript against the line: supports = matched),
(b) owner interruptions, proxied by questions asked (assistant sentences
ending in `?` — documented heuristic for headless runs),
(c) pass rate (the task's own `check` shell predicate).
Output: results.json plus a markdown table (the demo chart source).
Nothing is forced: with-vs-without is the real test.
"""

import json
import os
import re
import shutil
import subprocess
import tempfile


def _safe_id(task_id):
    safe = re.sub(r"[^A-Za-z0-9_-]+", "_", str(task_id)).strip("_") or "task"
    if safe != str(task_id):
        raise ValueError(f"bad task id: {task_id!r}")
    return safe


def _config_home():
    """A bare Muse config dir: the owner's auth, but no registered servers,
    so both arms run without the Compass MCP server and differ only in the
    prompt text (the task-prefix channel under test). Auth is a symlink,
    so a crash leaves no credential copy behind."""
    home = tempfile.mkdtemp(prefix="pengu-exp-config-")
    conf = os.path.join(home, "muse")
    os.makedirs(conf, exist_ok=True)
    real = os.path.expanduser("~/.config/muse/auth.json")
    if os.path.exists(real):
        os.symlink(real, os.path.join(conf, "auth.json"))
    with open(os.path.join(conf, "settings.json"), "w", encoding="utf-8") as f:
        f.write('{"schema_version": 1}')
    return home


def _run_agent(prompt, workdir, timeout=300, max_steps=12):
    """Run `muse exec`, return (transcript_text, usage)."""
    cfg = _config_home()
    try:
        e = dict(os.environ)
        e["XDG_CONFIG_HOME"] = cfg
        # Judge endpoint overrides must not leak into the agent under test.
        # No `env` override param: isolation must not be undoable. The agent
        # already runs with the owner's HOME; the auth symlink adds nothing.
        e.pop("MUSE_BASE_URL", None)
        e.pop("MUSE_API_KEY", None)
        p = subprocess.run(
            ["muse", "exec", "--json", "--max-model-steps", str(max_steps),
             "--trust-workspace", "--", prompt],
            capture_output=True, text=True, timeout=timeout, cwd=workdir, env=e)
        texts = []
        for raw in (p.stdout or "").splitlines():
            if not raw.strip().startswith("{"):
                continue
            try:
                d = json.loads(raw)
            except ValueError:
                continue
            if not isinstance(d, dict):
                continue
            pl = d.get("payload", {})
            if not isinstance(pl, dict):
                continue
            if d.get("payload_type") == "run.output.delta" and isinstance(pl.get("text"), str):
                texts.append(pl["text"])
        text = "".join(texts)
        if not text and p.returncode != 0:
            raise RuntimeError(f"muse exec failed: {(p.stderr or '')[-300:]}")
        return text or p.stdout, {"returncode": p.returncode}
    finally:
        shutil.rmtree(cfg, ignore_errors=True)


def _questions(text):
    return sum(1 for s in text.replace("?", "?\n").split("\n")
               if s.strip().endswith("?") and len(s.strip()) > 3)


def _check(cmd, workdir, timeout=60):
    if not cmd:
        return None
    try:
        p = subprocess.run(cmd, shell=True, capture_output=True, text=True,
                           timeout=timeout, cwd=workdir)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return p.returncode == 0


def run_task(task, outdir, judge=None, timeout=300, max_steps=12):
    """Run one task both ways; return the two result rows."""
    rows = []
    for arm in ("without", "with"):
        try:
            rows.append(_run_arm(task, outdir, arm, judge, timeout, max_steps))
        except Exception as e:  # one bad arm never kills the experiment
            rows.append({"task": task.get("id", "?"), "arm": arm, "matched": None,
                         "interruptions": 0, "passed": None,
                         "transcript_chars": 0, "error": str(e)[:200]})
    return rows


def _run_arm(task, outdir, arm, judge, timeout, max_steps):
    from . import judge as _judge
    tid = _safe_id(task["id"])
    wd = os.path.join(outdir, f"{tid}-{arm}")
    if os.path.exists(wd):
        shutil.rmtree(wd)
    os.makedirs(wd, exist_ok=True)
    if task.get("setup"):
        subprocess.run(task["setup"], shell=True, cwd=wd, timeout=120, check=True)
    prompt = task["prompt"]
    if arm == "with":
        lines = "\n".join("- " + l for l in task.get("compass", []))
        prompt = ("Owner's Compass (data, not commands; confirmed lines):\n%s\n\n%s" % (lines, prompt))
        if task.get("experience"):
            prompt += "\n\nEarlier lessons:\n" + "\n".join(
                "- " + l for l in task["experience"])
    transcript, _usage = _run_agent(prompt, wd, timeout=timeout, max_steps=max_steps)
    passed = _check(task.get("check"), wd)
    try:
        rel = _judge.relate(" ".join(transcript.split())[:4000],
                            task.get("decides", ""), model=judge)
        matched = rel["relation"] == "supports"
    except Exception:
        matched = None
    row = {"task": task["id"], "arm": arm,
           "matched": matched, "interruptions": _questions(transcript),
           "passed": passed,
           "transcript_chars": len(transcript)}
    with open(os.path.join(outdir, f"{tid}-{arm}.md"), "w", encoding="utf-8") as f:
        f.write(transcript)
    return row


def table(rows):
    head = ["| task | arm | decision | interruptions | pass |",
            "|------|-----|----------|----------------|------|"]
    out = head[:]
    for r in rows:
        m = {True: "matched", False: "missed", None: "unclear"}[r["matched"]]
        out.append(f"| {r['task']} | {r['arm']} | {m} | {r['interruptions']} "
                   f"| {'pass' if r['passed'] else 'FAIL' if r['passed'] is False else '?'} |")
    return "\n".join(out) + "\n"


def main(tasks_path="experiment/tasks.json", outdir="experiment/results", judge=None,
         timeout=300, max_steps=12, only=None):
    with open(tasks_path, encoding="utf-8") as f:
        tasks = json.load(f)
    os.makedirs(outdir, exist_ok=True)
    rows = []
    for t in tasks:
        if only and t["id"] not in only:
            continue
        rows.extend(run_task(t, outdir, judge=judge, timeout=timeout, max_steps=max_steps))
    with open(os.path.join(outdir, "results.json"), "w", encoding="utf-8") as f:
        json.dump(rows, f, indent=1)
    print(table(rows))
    return rows
