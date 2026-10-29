import {
  ChevronDown,
  ChevronRight,
  Copy,
  File,
  FilePlus,
  Folder,
  FolderOpen,
  FolderPlus,
  GitBranch,
  GitPullRequest,
  Pencil,
  RefreshCw,
  SquareTerminal,
  Trash2,
} from "lucide-react";
import { ContextMenu } from "radix-ui";
import { useEffect, useRef, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { getJSON, launchAgent, useApp } from "../app/ctx";
import { localDesktop } from "../app/desktop";
import { cn, fullTime, shortTime } from "../lib/utils";
import type { SessionView } from "../protocol/control";
import { PATH_MIME, absPath, badName, joinRel, parentDir } from "./explorerPaths";
import { ProposeSheet } from "./GitHub";
import { Item, MenuContent, Sep } from "./map/actions";

// Explorer follows the selected session: its real working directory, read through the host's
// bounded endpoints (os.Root, so nothing outside the workspace). Folder expansion, the mode and
// scroll position are kept per session; a late answer for a previous session is ignored.

interface Entry {
  name: string;
  dir: boolean;
  size?: number;
  link?: boolean;
  /** Last modified (RFC 3339). */
  mtime?: string;
}

interface Change {
  path: string;
  status: string;
  from?: string;
}

interface ChangesResp {
  git: boolean;
  branch?: string;
  toplevel?: string;
  changes?: Change[] | null;
  message?: string;
}

interface PerSession {
  mode: "files" | "changes";
  open: Record<string, boolean>;
  scroll: number;
}

/** What an open name field is for: a new file or folder inside `at`, or renaming the entry `at`. */
interface Edit {
  mode: "file" | "dir" | "rename";
  at: string;
}

/** A one-line name field, as in VS Code: Enter accepts, Escape or leaving it cancels. */
function NameField({ initial, depth, onDone }: { initial: string; depth: number; onDone: (name: string | null) => void }): ReactElement {
  const ref = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    ref.current?.focus();
    ref.current?.setSelectionRange(0, initial.lastIndexOf(".") > 0 ? initial.lastIndexOf(".") : initial.length);
  }, [initial]);
  return (
    <input
      ref={ref}
      defaultValue={initial}
      aria-label="Name"
      spellCheck={false}
      className="my-px h-6 w-[calc(100%-0.5rem)] rounded-sm border border-primary bg-background px-1.5 text-label outline-none"
      style={{ marginLeft: depth * 12 + 20 }}
      onKeyDown={(e) => {
        if (e.key === "Enter") onDone(e.currentTarget.value.trim());
        if (e.key === "Escape") onDone(null);
      }}
      onBlur={() => onDone(null)}
    />
  );
}

const memory = new Map<string, PerSession>();

const CHANGE_STYLE: Record<string, string> = {
  M: "text-warning",
  A: "text-success",
  U: "text-success",
  D: "text-danger",
  R: "text-info",
  C: "text-danger",
};

function statusLetter(s: string): string {
  if (s === "??") return "U";
  if (s.includes("U") || s === "AA" || s === "DD") return "C";
  return s.trim()[0] ?? "M";
}

export function Explorer({ session }: { session: SessionView | undefined }): ReactElement {
  const app = useApp();
  const id = session?.id ?? "";
  const [state, setState] = useState<PerSession>(() => memory.get(id) ?? { mode: "files", open: {}, scroll: 0 });
  const [dirs, setDirs] = useState<Record<string, Entry[] | string>>({});
  const [changes, setChanges] = useState<ChangesResp | null>(null);
  const [edit, setEdit] = useState<Edit | null>(null);
  const [target, setTarget] = useState<{ path: string; dir: boolean }>({ path: "", dir: true });
  const gen = useRef(0);
  const scroller = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    gen.current++;
    setState(memory.get(id) ?? { mode: "files", open: {}, scroll: 0 });
    setDirs({});
    setChanges(null);
    requestAnimationFrame(() => {
      if (scroller.current !== null) scroller.current.scrollTop = memory.get(id)?.scroll ?? 0;
    });
  }, [id]);

  useEffect(() => {
    if (id !== "") memory.set(id, state);
  }, [id, state]);

  const load = async (path: string): Promise<void> => {
    const g = gen.current;
    try {
      const r = await getJSON<{ entries: Entry[] | null }>(app.conn, "/api/files", { session: id, path });
      if (g === gen.current) setDirs((d) => ({ ...d, [path]: r.entries ?? [] }));
    } catch (e) {
      if (g === gen.current) setDirs((d) => ({ ...d, [path]: (e as Error).message }));
    }
  };

  const loadChanges = async (): Promise<void> => {
    const g = gen.current;
    try {
      const r = await getJSON<ChangesResp>(app.conn, "/api/changes", { session: id });
      if (g === gen.current) setChanges(r);
    } catch (e) {
      if (g === gen.current) setChanges({ git: false, message: (e as Error).message });
    }
  };

  const refresh = (): void => {
    if (id === "") return;
    if (state.mode === "files") {
      void load("");
      for (const [p, o] of Object.entries(state.open)) if (o) void load(p);
    } else void loadChanges();
  };

  // Changes on disk invalidate what is shown on a gentle interval (no whole-repo scan per keystroke).
  useEffect(() => {
    refresh();
    const t = window.setInterval(refresh, 5000);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, state.mode]);

  if (session === undefined) return <p className="px-3 py-2 text-caption text-muted-foreground">Select a session to browse its workspace.</p>;

  const cwd = session.cwd;
  const folderOf = (t: { path: string; dir: boolean }): string => (t.dir ? t.path : parentDir(t.path));
  const reload = (dir: string): void => void load(dir);
  const startNew = (mode: "file" | "dir"): void => {
    const dir = folderOf(target);
    if (dir !== "" && !state.open[dir]) setState({ ...state, open: { ...state.open, [dir]: true } });
    if (dirs[dir] === undefined) reload(dir);
    setEdit({ mode, at: dir });
  };
  const finish = async (name: string | null): Promise<void> => {
    const e = edit;
    setEdit(null);
    if (e === null || name === null || name === "") return;
    if (badName(name)) return void toast.error("A name is one file or folder name, without “/”.");
    if (e.mode === "rename") {
      if (name === e.at.split("/").pop()) return;
      const to = joinRel(parentDir(e.at), name);
      if ((await app.call("fs.rename", { session_id: id, path: e.at, to })) !== undefined) reload(parentDir(e.at));
    } else if ((await app.call("fs.create", { session_id: id, path: joinRel(e.at, name), dir: e.mode === "dir" })) !== undefined) {
      reload(e.at);
    }
  };
  const trash = async (): Promise<void> => {
    if ((await app.call("fs.trash", { session_id: id, path: target.path })) !== undefined) {
      toast.success(`Moved ${target.path.split("/").pop()} to the Trash`);
      reload(parentDir(target.path));
    }
  };
  const openTerminal = async (): Promise<void> => {
    const dir = folderOf(target);
    const base = `${(dir === "" ? cwd : dir).split("/").filter(Boolean).pop() ?? "folder"}-shell`;
    const taken = new Set(app.sessions.map((x) => x.name));
    let name = base;
    for (let i = 2; taken.has(name); i++) name = `${base}-${i}`;
    const s = await app.call<SessionView>("session.launch", { name, ...launchAgent("shell"), source: { kind: "local", path: absPath(cwd, dir) } });
    if (s !== undefined) app.select(s.id);
  };
  const copy = (text: string): void => {
    void navigator.clipboard.writeText(text);
    toast.success("Path copied");
  };

  const toggle = (p: string): void => {
    const next = !state.open[p];
    setState({ ...state, open: { ...state.open, [p]: next } });
    if (next && dirs[p] === undefined) void load(p);
  };
  const list = changes?.changes ?? [];

  const tree = (path: string, depth: number): ReactElement => {
    const entries = dirs[path];
    const pad = { paddingLeft: depth * 12 + 8 };
    if (entries === undefined)
      return (
        <p className="py-0.5 text-caption text-faint" style={pad}>
          Loading…
        </p>
      );
    if (typeof entries === "string")
      return (
        <p className="py-0.5 text-caption text-danger" style={pad}>
          {entries}
        </p>
      );
    return (
      <ul role={depth === 0 ? "tree" : "group"}>
        {edit !== null && edit.mode !== "rename" && edit.at === path ? <NameField initial="" depth={depth} onDone={(n) => void finish(n)} /> : null}
        {entries.map((e) => {
          const p = path === "" ? e.name : `${path}/${e.name}`;
          const open = state.open[p] === true;
          if (edit?.mode === "rename" && edit.at === p)
            return (
              <li key={p}>
                <NameField initial={e.name} depth={depth} onDone={(n) => void finish(n)} />
              </li>
            );
          return (
            <li key={p} role="treeitem" aria-expanded={e.dir ? open : undefined}>
              <button
                type="button"
                data-path={p}
                data-dir={e.dir}
                draggable
                onDragStart={(ev) => {
                  ev.dataTransfer.setData(PATH_MIME, absPath(cwd, p));
                  ev.dataTransfer.setData("text/plain", absPath(cwd, p));
                  ev.dataTransfer.effectAllowed = "copy";
                }}
                className="flex h-6 w-full items-center gap-1 rounded-sm pr-2 text-label text-muted-foreground hover:bg-hover hover:text-foreground"
                style={{ paddingLeft: depth * 12 + 4 }}
                onClick={() => (e.dir ? toggle(p) : app.openTab({ key: `file:${id}:${p}`, kind: "file", sessionId: id, path: p, title: e.name }))}
                title={e.mtime !== undefined ? `${p}\nModified ${fullTime(e.mtime)}` : p}
              >
                {e.dir ? open ? <ChevronDown className="size-3.5 shrink-0" /> : <ChevronRight className="size-3.5 shrink-0" /> : <span className="w-3.5 shrink-0" />}
                {e.dir ? <Folder className="size-3.5 shrink-0 text-primary" /> : <File className="size-3.5 shrink-0 text-faint" />}
                <span className="truncate">{e.name}</span>
                {e.link === true ? <span className="text-micro text-faint">↗</span> : null}
                {e.mtime !== undefined ? <span className="ml-auto shrink-0 pl-2 text-micro tabular-nums text-faint">{shortTime(e.mtime)}</span> : null}
              </button>
              {e.dir && open ? tree(p, depth + 1) : null}
            </li>
          );
        })}
      </ul>
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-1 px-2 pb-1">
        {(["files", "changes"] as const).map((m) => (
          <button
            key={m}
            type="button"
            onClick={() => setState({ ...state, mode: m })}
            aria-pressed={state.mode === m}
            className={cn(
              "h-6 rounded-md px-2 text-caption font-medium text-muted-foreground hover:bg-hover",
              state.mode === m && "bg-panel text-foreground shadow-sm ring-1 ring-border",
            )}
          >
            {m === "files" ? "Files" : `Changes${list.length > 0 ? ` · ${list.length}` : ""}`}
          </button>
        ))}
        <button type="button" onClick={refresh} className="ml-auto rounded p-1 text-faint hover:bg-hover" aria-label="Refresh">
          <RefreshCw className="size-3.5" />
        </button>
      </div>
      <div className="truncate px-3 pb-1 font-mono text-micro text-faint" title={session.cwd}>
        {session.cwd}
      </div>
      <ContextMenu.Root>
        <ContextMenu.Trigger asChild disabled={state.mode !== "files"}>
          <div
            className="min-h-0 flex-1 overflow-auto px-1 pb-2"
            ref={scroller}
            onContextMenuCapture={(ev) => {
              const row = (ev.target as HTMLElement).closest<HTMLElement>("[data-path]");
              setTarget(row === null ? { path: "", dir: true } : { path: row.dataset.path ?? "", dir: row.dataset.dir === "true" });
            }}
            onScroll={(e) => {
              const top = (e.target as HTMLDivElement).scrollTop;
              setState((s) => ({ ...s, scroll: top }));
            }}
          >
            {state.mode === "files" ? (
              tree("", 0)
            ) : changes === null ? (
              <p className="px-2 text-caption text-faint">Loading…</p>
            ) : !changes.git ? (
              <p className="px-2 text-caption text-muted-foreground">Not a Git repository.</p>
            ) : (
              <>
                <p className="flex items-center gap-1.5 px-2 pb-1 text-caption text-muted-foreground">
                  <GitBranch className="size-3.5" />
                  <span className="truncate font-mono">{changes.branch}</span>
                  {changes.toplevel !== undefined ? (
                    <button
                      type="button"
                      onClick={() => app.showDialog(<ProposeSheet path={changes.toplevel ?? ""} branch={changes.branch} />)}
                      className="ml-auto flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-caption text-muted-foreground hover:bg-hover hover:text-foreground"
                      title="Open a pull request with the committed work"
                    >
                      <GitPullRequest className="size-3.5" /> Propose
                    </button>
                  ) : null}
                </p>
                {list.length === 0 ? <p className="px-2 text-caption text-faint">No changes.</p> : null}
                {(["staged", "working"] as const).map((kind) => {
                  const rows = list.filter((c) => (kind === "staged" ? c.status[0] !== " " && c.status[0] !== "?" : c.status[1] !== " "));
                  if (rows.length === 0) return null;
                  return (
                    <div key={kind} className="mb-1">
                      <p className="px-2 pt-1 text-micro font-semibold uppercase tracking-wider text-faint">{kind === "staged" ? "Staged" : "Changes"}</p>
                      {rows.map((c) => {
                        const letter = statusLetter(kind === "staged" ? c.status[0] + " " : c.status);
                        return (
                          <button
                            key={kind + c.path}
                            type="button"
                            className="flex h-6 w-full items-center gap-2 rounded-sm px-2 text-label text-muted-foreground hover:bg-hover hover:text-foreground"
                            onClick={() =>
                              app.openTab({
                                key: `diff:${id}:${kind}:${c.path}`,
                                kind: "diff",
                                sessionId: id,
                                path: c.path,
                                title: c.path.split("/").pop() ?? c.path,
                                staged: kind === "staged",
                              })
                            }
                            title={c.from !== undefined ? `${c.from} → ${c.path}` : c.path}
                          >
                            <span className="truncate">{c.path.split("/").pop()}</span>
                            <span className="truncate text-micro text-faint">{c.path.split("/").slice(0, -1).join("/")}</span>
                            <span className={cn("ml-auto font-mono text-caption font-semibold", CHANGE_STYLE[letter])}>{letter}</span>
                          </button>
                        );
                      })}
                    </div>
                  );
                })}
              </>
            )}
          </div>
        </ContextMenu.Trigger>
        <MenuContent>
          <Item icon={<Copy />} onSelect={() => copy(absPath(cwd, target.path))}>
            Copy path
          </Item>
          <Item icon={<Copy />} disabled={target.path === ""} onSelect={() => copy(target.path)}>
            Copy relative path
          </Item>
          <Sep />
          <Item icon={<FilePlus />} onSelect={() => startNew("file")}>
            New file…
          </Item>
          <Item icon={<FolderPlus />} onSelect={() => startNew("dir")}>
            New folder…
          </Item>
          <Item icon={<Pencil />} disabled={target.path === ""} onSelect={() => setEdit({ mode: "rename", at: target.path })}>
            Rename…
          </Item>
          <Item icon={<Trash2 />} danger disabled={target.path === ""} onSelect={() => void trash()}>
            Delete (move to Trash)
          </Item>
          <Sep />
          <Item icon={<FolderOpen />} disabled={localDesktop === undefined} onSelect={() => localDesktop?.revealPath(absPath(cwd, target.path))}>
            Reveal in Finder
          </Item>
          <Item icon={<SquareTerminal />} onSelect={() => void openTerminal()}>
            Open in terminal
          </Item>
        </MenuContent>
      </ContextMenu.Root>
    </div>
  );
}
