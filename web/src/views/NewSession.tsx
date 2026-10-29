import { FolderOpen, GitBranch } from "lucide-react";
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

// New session: an agent runtime (Claude Code, OMP, pi, Codex or Copilot) in a folder. Two paths, as the plan defines
// them: use a local repository, folder or existing worktree as it is, or clone a Git repository into
// a new destination. A parent puts the new session under that session on the map; it may run a
// different agent, since team messages go through PenguPool.

export function NewSessionDialog({ onClose, initialPath, parentId }: { onClose: () => void; initialPath?: string; parentId?: string }): ReactElement {
  const app = useApp();
  const parent = app.sessions.find((s) => s.id === parentId);
  const [kind, setKind] = useState<"local" | "clone">("local");
  const [savedPath, setSavedPath] = usePersisted("new.path", app.domain.repos[0]?.path ?? "");
  const [path, setPathState] = useState(initialPath ?? parent?.cwd ?? savedPath);
  const setPath = (v: string): void => {
    setPathState(v);
    setSavedPath(v);
  };
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
  const [model, setModel] = useState("");
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
    const base = (kind === "clone" ? (url.split("/").pop() ?? "repo").replace(/\.git$/, "") : (path.split("/").filter(Boolean).pop() ?? "session")) + `-${agent}`;
    const taken = new Set(app.sessions.map((s) => s.name));
    if (!taken.has(base)) return base;
    for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
  };

  const submit = async (): Promise<void> => {
    setBusy(true);
    const s = await app.call<SessionView>("session.launch", {
      name: name.trim() === "" ? defaultName() : name.trim(),
      ...launchAgent(harness, model),
      source: kind === "clone" ? { kind, url, dest } : { kind: "local", path },
    });
    if (s !== undefined && parentId !== undefined) await app.call("team.group", { session_id: s.id, parent_id: parentId });
    setBusy(false);
    if (s !== undefined) {
      app.select(s.id);
      onClose();
    }
  };

  const valid =
    // Not before the install check answers: until then the pick may still change.
    (kind === "local" ? path.trim() !== "" : url.trim() !== "" && dest.trim() !== "") && pickReady(found, ollamaDefault !== null) && harness !== "";

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
            {busy ? (kind === "clone" ? "Cloning…" : "Starting…") : "Start session"}
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
              ["clone", "Clone repository", <GitBranch key="g" className="size-4" />],
            ] as const
          ).map(([k, label, icon]) => (
            <button
              key={k}
              type="button"
              onClick={() => setKind(k)}
              aria-pressed={kind === k}
              className={cn("flex h-7 flex-1 items-center justify-center gap-1.5 rounded-md", kind === k ? "bg-panel font-medium shadow-sm" : "text-muted-foreground")}
            >
              {icon}
              {label}
            </button>
          ))}
        </div>

        {kind === "local" ? (
          <Label hint="Used as is: no clone, no git init. An existing worktree stays a worktree.">
            Folder
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
        ) : (
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
        )}

        <div className="grid grid-cols-2 gap-3">
          <Label>
            Name
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={valid ? defaultName() : "defaults to the folder"} />
          </Label>
          {!onOllama ? ( // Claude Code (Ollama) runs on Ollama's own default model
            <Label>
              Model <span className="font-normal text-faint">optional</span>
              <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder="harness default" />
            </Label>
          ) : null}
        </div>
        {agent === "claude" ? (
          <p className="rounded-md bg-muted p-2.5 text-caption text-muted-foreground">The first time Claude Code opens a folder it asks whether you trust it. Answer in the terminal below.</p>
        ) : null}
        <button type="submit" className="hidden" />
      </form>
    </Sheet>
  );
}
