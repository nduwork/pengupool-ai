import { FolderGit2, FolderOpen, GitBranch } from "lucide-react";
import { useEffect, useId, useState, type ReactElement } from "react";

import { AGENTS, CLAUDE_OLLAMA, agentOptions, checkFailed, choosePick, harnessLabel, pickReady, launchAgent, parentPick, pickable, useApp, usePersisted, type Found } from "../app/ctx";
import { localDesktop } from "../app/desktop";
import { FolderBrowser } from "../components/FolderBrowser";
import { Button } from "../components/ui/button";
import { Input, Label, Select } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import { cn } from "../lib/utils";
import type { SessionView } from "../protocol/control";
import { GitHubRepoPicker } from "./GitHub";

// New session: an agent runtime (Claude Code, OMP, pi, Codex or Copilot) in a folder. Three paths: use a
// local repository, folder or existing worktree as it is, start in a new worktree of a local repository
// (in PenguPool's own folder, on a new or an existing branch), or clone a Git repository into a new
// destination. A parent puts the new session under that session on the map; it may run a
// different agent, since team messages go through PenguPool.

export function NewSessionDialog({ onClose, initialPath, parentId }: { onClose: () => void; initialPath?: string; parentId?: string }): ReactElement {
  const app = useApp();
  const parent = app.sessions.find((s) => s.id === parentId);
  const [kind, setKind] = useState<"local" | "worktree" | "clone">("local");
  const [savedPath, setSavedPath] = usePersisted("new.path", app.domain.repos[0]?.path ?? "");
  const [path, setPathState] = useState(initialPath ?? parent?.cwd ?? savedPath);
  const setPath = (v: string): void => {
    setPathState(v);
    setSavedPath(v);
  };
  // New worktree: a new branch (its name defaults to pengupool/<session name>) from a branch of the
  // repository, or an existing branch that no other worktree has checked out.
  const [newBranch, setNewBranch] = useState(true);
  const [branch, setBranch] = useState<string | null>(null); // null: the default name, until the owner types one
  const [from, setFrom] = useState("");
  const [existing, setExisting] = useState("");
  // null until repo.branches answers, so "No branches here" never shows for a repository still loading.
  const [branchList, setBranches] = useState<string[] | null>(null);
  const branches = branchList ?? [];
  const [inUse, setInUse] = useState<Map<string, string>>(new Map());
  useEffect(() => {
    // Another repository: nothing picked in the last one carries over.
    setBranch(null);
    setFrom("");
    setExisting("");
    setBranches(null);
    setInUse(new Map());
    if (kind !== "worktree" || path.trim() === "") return;
    let live = true;
    void app.conn.request<string[]>("repo.branches", { path: path.trim() }).then(
      (bs) => live && setBranches(bs),
      () => live && setBranches(null),
    );
    void app.conn.request<{ path: string; branch: string }[]>("repo.worktrees", { path: path.trim() }).then(
      (ws) => live && setInUse(new Map(ws.filter((w) => w.branch !== "").map((w) => [w.branch, app.sessions.find((s) => s.cwd === w.path)?.name ?? (w.path.split("/").pop() ?? w.path)]))),
      () => live && setInUse(new Map()),
    );
    return () => {
      live = false;
    };
  }, [kind, path]);
  const [url, setUrl] = useState("");
  const [dest, setDest] = useState("");
  // A new key: a pick remembered before (a plain "claude") must not beat Claude Code (Ollama) when
  // the Ollama setting made it the default.
  const [savedHarness, setSavedHarness] = usePersisted("new.agent", "");
  // A child starts on its parent's agent (through Ollama when its parent was) unless another is picked.
  const [want, setHarnessState] = useState<string>(parentPick(parent) ?? savedHarness);
  const setHarness = (h: string): void => {
    setHarnessState(h);
    setSavedHarness(h);
  };
  // Which agents are installed on this machine (version, or why not), and whether Claude Code (Ollama)
  // is the default.
  const [found, setFound] = useState<Found | null>(null);
  const [probeFailed, setProbeFailed] = useState(false);
  const [ollamaDefault, setOllamaDefault] = useState<boolean | null>(null); // null until settings.get answers
  useEffect(() => {
    void app.conn.request<Found>("harness.probe").then(setFound, () => {
      setProbeFailed(true);
      setFound(checkFailed());
    });
    void app.conn.request<{ ollama_default?: boolean }>("settings.get").then(
      (r) => setOllamaDefault(r.ollama_default === true),
      () => setOllamaDefault(false),
    );
  }, []);
  // What is shown is what is sent: a remembered pick that is gone gives way to Claude Code (Ollama)
  // when it is the default, else the first installed agent.
  const options = pickable(found, AGENTS, true);
  const harness = choosePick(options, want, ollamaDefault === true);
  const onOllama = harness === CLAUDE_OLLAMA;
  const agent = onOllama ? "claude" : harness;
  const hintId = useId();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const recent = [...new Set([...app.domain.repos.map((r) => r.path), ...app.sessions.map((s) => s.cwd)])];

  // Where a picked repository is cloned: next to the last destination, else next to a known repository.
  const cloneParent = (): string => {
    const from = dest !== "" ? dest : (app.domain.repos[0]?.path ?? "");
    const i = from.lastIndexOf("/");
    return i > 0 ? from.slice(0, i) : "~/repos";
  };

  // "Choose…": the desktop app's native picker on this machine, the host's own folder browser
  // everywhere else (an SSH host, a browser), where the picker cannot reach.
  const [browsing, setBrowsing] = useState<"path" | "dest" | null>(null);
  const choose = async (which: "path" | "dest"): Promise<void> => {
    if (localDesktop === undefined) {
      setBrowsing(which);
      return;
    }
    const p = await localDesktop.pickFolder();
    if (p === null || p === undefined) return;
    if (which === "path") setPath(p);
    else setDest(`${p}/${(url.split("/").pop() ?? "repo").replace(/\.git$/, "")}`);
  };

  // A distinct default name, so two sessions in one folder are told apart.
  const defaultName = (): string => {
    const base = (kind === "clone" ? (url.split("/").filter(Boolean).pop() ?? "repo").replace(/\.git$/, "") : (path.split("/").filter(Boolean).pop() ?? "session")) + `-${agent}`;
    const taken = new Set(app.sessions.map((s) => s.name));
    if (!taken.has(base)) return base;
    for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
  };

  const sessionName = (): string => (name.trim() === "" ? defaultName() : name.trim());
  // The default branch name is free: a closed session keeps its branch, so a name in use gets -2, -3…
  const defaultBranch = (): string => {
    const base = `pengupool/${sessionName().toLowerCase().replace(/[^a-z0-9._-]+/g, "-")}`;
    if (!branches.includes(base)) return base;
    for (let i = 2; ; i++) if (!branches.includes(`${base}-${i}`)) return `${base}-${i}`;
  };
  const branchName = (): string => (branch === null ? defaultBranch() : branch.trim());
  const branchTaken = newBranch && branch !== null && branches.includes(branch.trim());
  const source = () => {
    if (kind === "clone") return { kind, url, dest };
    if (kind === "local") return { kind, path };
    // repo.branches lists the repository's default branch first, so it is From's default.
    return newBranch ? { kind, path, branch: branchName(), new_branch: true, from: from !== "" ? from : branches[0] } : { kind, path, branch: existing };
  };

  const submit = async (): Promise<void> => {
    setBusy(true);
    const s = await app.call<SessionView>("session.launch", { name: sessionName(), ...launchAgent(harness), source: source() });
    if (s !== undefined && parentId !== undefined) await app.call("team.group", { session_id: s.id, parent_id: parentId });
    setBusy(false);
    if (s !== undefined) {
      app.select(s.id);
      onClose();
    }
  };

  const valid =
    // Not before the install check answers: until then the pick may still change.
    (kind === "clone" ? url.trim() !== "" && dest.trim() !== "" : path.trim() !== "" && (kind === "local" || (branches.length > 0 && (newBranch ? branchName() !== "" && !branchTaken : existing !== "")))) && pickReady(found, ollamaDefault !== null) && harness !== "";

  return (
    <Sheet
      title={parent !== undefined ? `New session under ${parent.name}` : "New session"}
      description={
        parent !== undefined ? `Joins ${parent.name}'s team on the map (${harnessLabel(parent.harness)}); any agent can be its child.` : "Start an agent in a repository, folder or worktree."
      }
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="default" disabled={busy || !valid} onClick={() => void submit()}>
            {busy ? (kind === "clone" ? "Cloning…" : kind === "worktree" ? "Making the worktree…" : "Starting…") : "Start session"}
          </Button>
        </>
      }
    >
      <form
        className="grid grid-cols-1 gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) void submit();
        }}
      >
        <Label
          hintId={hintId}
          hint={
            found === null
              ? "Checking which agents are installed…"
              : options.length === 0
                ? "No agent is installed here. Install one; Settings › Agents shows which."
                : [
                    AGENTS.find((a) => a.id === agent)?.hint,
                    found[agent]?.version,
                    onOllama ? `through Ollama on ${found.ollama?.model ?? "its default model"}` : undefined,
                    // Claude Code (Ollama) shown but unable to start: why (Ollama down, the model to pull).
                    ((d) => (!onOllama && d !== undefined ? `Claude Code (Ollama): ${d}` : undefined))(options.find((o) => o.value === CLAUDE_OLLAMA)?.disabled),
                    probeFailed ? "Couldn't check which agents are installed; all are offered." : undefined,
                  ]
                    .filter((x) => x !== undefined && x !== "")
                    .join(" · ")
          }
        >
          Agent
          <Select value={harness} onChange={(e) => setHarness(e.target.value)} aria-label="Agent runtime" aria-describedby={hintId} autoFocus>
            {agentOptions(options, (v) => (parent !== undefined && parentPick(parent) === v ? " (same as parent)" : ""))}
          </Select>
        </Label>

        <div className="flex gap-1 rounded-lg bg-muted p-1 text-label">
          {(
            [
              ["local", "Local folder", <FolderOpen key="f" className="size-4" />],
              ["worktree", "New worktree", <FolderGit2 key="w" className="size-4" />],
              ["clone", "Clone repository", <GitBranch key="g" className="size-4" />],
            ] as const
          ).map(([k, label, icon]) => (
            <button
              key={k}
              type="button"
              onClick={() => setKind(k)}
              aria-pressed={kind === k}
              className={cn("flex h-7 flex-1 items-center justify-center gap-1.5 rounded-md focus-visible:outline-2 focus-visible:outline-foreground dark:focus-visible:outline-ring", kind === k ? "bg-panel font-medium shadow-sm dark:ring-1 dark:ring-faint" : "text-muted-foreground")}
            >
              {icon}
              {label}
            </button>
          ))}
        </div>

        {kind !== "clone" ? (
          <Label hint={kind === "local" ? "Used as is: no clone, no git init. An existing worktree stays a worktree." : "A local Git repository. The worktree goes in PenguPool's own folder; the repository's folder is left as it is."}>
            {kind === "local" ? "Folder" : "Repository"}
            <div className="flex gap-2">
              <Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/Users/you/repos/project" list="pp-recent" className="font-mono" />
              <Button onClick={() => void choose("path")} aria-expanded={browsing === "path"}>
                <FolderOpen /> Choose…
              </Button>
            </div>
            {browsing === "path" ? (
              <FolderBrowser
                start={path.trim() === "" ? "~" : path}
                onPick={(p) => {
                  setPath(p);
                  setBrowsing(null);
                }}
                onCancel={() => setBrowsing(null)}
              />
            ) : null}
            <datalist id="pp-recent">
              {recent.map((p) => (
                <option key={p} value={p} />
              ))}
            </datalist>
          </Label>
        ) : null}
        {kind === "worktree" ? (
          <>
            <div className="flex gap-1 rounded-lg bg-muted p-1 text-label">
              {(
                [
                  [true, "New branch"],
                  [false, "Existing branch"],
                ] as const
              ).map(([k, label]) => (
                <button
                  key={label}
                  type="button"
                  onClick={() => setNewBranch(k)}
                  aria-pressed={newBranch === k}
                  className={cn("flex h-7 flex-1 items-center justify-center rounded-md focus-visible:outline-2 focus-visible:outline-foreground dark:focus-visible:outline-ring", newBranch === k ? "bg-panel font-medium shadow-sm dark:ring-1 dark:ring-faint" : "text-muted-foreground")}
                >
                  {label}
                </button>
              ))}
            </div>
            {newBranch ? (
              <div className="grid grid-cols-2 items-start gap-3">
                <Label hint={branchTaken ? `${branch?.trim()} already exists. Pick another name, or choose it under Existing branch.` : undefined}>
                  Branch
                  <Input value={branchName()} onChange={(e) => setBranch(e.target.value)} aria-invalid={branchTaken} className="font-mono" />
                </Label>
                <Label hint={branchList?.length === 0 ? "No branches here. Choose a Git repository with at least one commit." : undefined}>
                  From
                  <Select value={from !== "" ? from : (branches[0] ?? "")} onChange={(e) => setFrom(e.target.value)} disabled={branches.length === 0}>
                    {branches.map((b) => (
                      <option key={b} value={b}>
                        {b}
                      </option>
                    ))}
                  </Select>
                </Label>
              </div>
            ) : (
              <Label hint="A branch checked out in another worktree can't be checked out again.">
                Branch
                <Select value={existing} onChange={(e) => setExisting(e.target.value)}>
                  <option value="" disabled>
                    {branchList?.length === 0 ? "No branches found" : "Choose a branch"}
                  </option>
                  {branches.map((b) => (
                    <option key={b} value={b} disabled={inUse.has(b)}>
                      {inUse.has(b) ? `${b} (in use by ${inUse.get(b)})` : b}
                    </option>
                  ))}
                </Select>
              </Label>
            )}
          </>
        ) : null}
        {kind === "clone" ? (
          <>
            <GitHubRepoPicker
              selected={url}
              onPick={(r) => {
                setUrl(r.clone_url);
                setDest(`${cloneParent()}/${r.full_name.split("/")[1]}`);
              }}
            />
            <Label hint="Or paste any https, ssh or git URL. https github.com URLs use your signed-in account; nothing ever prompts for credentials.">
              Git URL
              <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="git@github.com:org/repo.git" className="font-mono" />
            </Label>
            <Label hint="Must not exist yet; PenguPool never overwrites a folder.">
              Destination
              <div className="flex gap-2">
                <Input value={dest} onChange={(e) => setDest(e.target.value)} placeholder="/Users/you/repos/repo" className="font-mono" />
                <Button onClick={() => void choose("dest")} aria-expanded={browsing === "dest"}>
                  Parent…
                </Button>
              </div>
              {browsing === "dest" ? (
                <FolderBrowser
                  start={cloneParent()}
                  onPick={(p) => {
                    setDest(`${p}/${(url.split("/").pop() ?? "repo").replace(/\.git$/, "")}`);
                    setBrowsing(null);
                  }}
                  onCancel={() => setBrowsing(null)}
                />
              ) : null}
            </Label>
          </>
        ) : null}

        <Label>
          Name
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={path.trim() !== "" || kind === "clone" ? defaultName() : "defaults to the folder"} />
        </Label>
        {agent === "claude" ? (
          <p className="rounded-md bg-muted p-2.5 text-caption text-muted-foreground">The first time Claude Code opens a folder it asks whether you trust it. Answer in the terminal below.</p>
        ) : null}
        <button type="submit" className="hidden" />
      </form>
    </Sheet>
  );
}
