import { FolderOpen, GitBranch } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";

import { AGENTS, CLAUDE_OLLAMA, agentOptions, harnessLabel, launchAgent, useApp, usePersisted } from "../app/ctx";
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
  const [savedHarness, setSavedHarness] = usePersisted("new.harness", "claude");
  // A child starts on its parent's agent unless another is picked.
  const [harness, setHarnessState] = useState<string>(
    AGENTS.some((a) => a.id === parent?.harness) ? (parent?.harness ?? "claude") : AGENTS.some((a) => a.id === savedHarness) || savedHarness === CLAUDE_OLLAMA ? savedHarness : "claude",
  );
  const setHarness = (h: string): void => {
    setHarnessState(h);
    setSavedHarness(h);
  };
  // Which agents are installed on this machine (version, or why not).
  const [found, setFound] = useState<Record<string, { version: string; error?: string }> | null>(null);
  useEffect(() => {
    void app.conn.request<Record<string, { version: string; error?: string }>>("harness.probe").then(setFound, () => setFound({}));
  }, []);
  const [model, setModel] = useState("");
  // Which agents run on a local Ollama model (Settings → Models & agents → Agents on this machine).
  const [models, setModels] = useState<{ ollama_model?: string; ollama?: string[] | null } | null>(null);
  useEffect(() => {
    void app.conn.request<{ agent_models?: { ollama_model?: string; ollama?: string[] | null } }>("settings.get").then(
      (r) => setModels(r.agent_models ?? null),
      () => undefined,
    );
  }, []);
  const onOllama =
    harness === CLAUDE_OLLAMA ? model.trim() || models?.ollama_model || "name a model below" : (models?.ollama ?? []).includes(harness) ? model.trim() || models?.ollama_model || "" : "";
  const missing = (id: string): boolean => (id === CLAUDE_OLLAMA ? missing("claude") : found !== null && found[id] !== undefined && found[id].error !== undefined);
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
    const base = (kind === "clone" ? (url.split("/").pop() ?? "repo").replace(/\.git$/, "") : (path.split("/").filter(Boolean).pop() ?? "session")) + `-${launchAgent(harness, "").harness}`;
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
    (kind === "local" ? path.trim() !== "" : url.trim() !== "" && dest.trim() !== "") &&
    !missing(harness) &&
    (harness !== CLAUDE_OLLAMA || model.trim() !== "" || (models?.ollama_model ?? "") !== "");

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
          hint={
            found === null
              ? "Checking which agents are installed…"
              : (found[launchAgent(harness, "").harness]?.error ??
                `${AGENTS.find((a) => a.id === launchAgent(harness, "").harness)?.hint ?? ""} · ${found[launchAgent(harness, "").harness]?.version ?? ""}${onOllama !== "" ? ` · runs on Ollama: ${onOllama}` : ""}`)
          }
        >
          Agent
          <Select value={harness} onChange={(e) => setHarness(e.target.value)} aria-label="Agent runtime" autoFocus>
            {agentOptions(AGENTS, missing, (id) => (parent !== undefined && parent.harness === id ? " (same as parent)" : ""))}
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
          <Label>
            Model <span className="font-normal text-faint">{harness === CLAUDE_OLLAMA && (models?.ollama_model ?? "") === "" ? "required" : "optional"}</span>
            <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder={harness === CLAUDE_OLLAMA ? (models?.ollama_model ?? "an Ollama model, e.g. qwen3-coder") : "harness default"} />
          </Label>
        </div>
        {launchAgent(harness, "").harness === "claude" ? (
          <p className="rounded-md bg-muted p-2.5 text-caption text-muted-foreground">The first time Claude Code opens a folder it asks whether you trust it. Answer in the terminal below.</p>
        ) : null}
        <button type="submit" className="hidden" />
      </form>
    </Sheet>
  );
}
