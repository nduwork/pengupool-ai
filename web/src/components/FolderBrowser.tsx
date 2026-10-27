import { ArrowUp, Folder, GitBranch, Home } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";

import { useApp } from "../app/ctx";
import { Button } from "./ui/button";

interface Dirs {
  path: string;
  parent?: string;
  home: string;
  dirs: { name: string; git?: boolean }[];
}

// A folder browser over the host's fs.dirs: it works wherever the host runs (this machine, an SSH host)
// and in a browser, where the desktop app's native picker does not reach. Inline, not a dialog:
// New session is already one.
export function FolderBrowser({ start, onPick, onCancel }: { start: string; onPick: (path: string) => void; onCancel: () => void }): ReactElement {
  const app = useApp();
  const [at, setAt] = useState(start);
  const [d, setD] = useState<Dirs | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    void app.call<Dirs>("fs.dirs", { path: at }).then((r) => {
      if (!live) return;
      if (r === undefined) {
        setErr(`Can't open ${at}. Pick another folder.`);
        return;
      }
      setErr("");
      setD(r);
    });
    return () => {
      live = false;
    };
  }, [at]);
  const join = (name: string): string => (d === null ? name : `${d.path.replace(/\/$/, "")}/${name}`);
  return (
    <div className="grid gap-2 rounded-md border border-border bg-raised p-2" role="group" aria-label="Choose a folder">
      <div className="flex items-center gap-1">
        <Button size="sm" variant="ghost" disabled={d?.parent === undefined} onClick={() => d?.parent !== undefined && setAt(d.parent)} aria-label="Up one folder">
          <ArrowUp />
        </Button>
        <Button size="sm" variant="ghost" onClick={() => d !== null && setAt(d.home)} aria-label="Home folder">
          <Home />
        </Button>
        <span className="min-w-0 flex-1 truncate font-mono text-caption" title={d?.path}>
          {d?.path ?? at}
        </span>
      </div>
      {err !== "" ? <p className="text-caption text-destructive">{err}</p> : null}
      <ul className="max-h-56 overflow-auto">
        {(d?.dirs ?? []).map((x) => (
          <li key={x.name}>
            <button
              type="button"
              className="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-label hover:bg-hover focus-visible:outline-2 focus-visible:outline-primary"
              onDoubleClick={() => onPick(join(x.name))}
              onClick={() => setAt(join(x.name))}
            >
              {x.git === true ? <GitBranch className="size-4 text-primary" aria-label="Git repository" /> : <Folder className="size-4 text-muted-foreground" />}
              <span className="truncate">{x.name}</span>
            </button>
          </li>
        ))}
        {d !== null && d.dirs.length === 0 ? <li className="px-2 py-1 text-caption text-muted-foreground">No folders here.</li> : null}
      </ul>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" disabled={d === null} onClick={() => d !== null && onPick(d.path)}>
          Use this folder
        </Button>
      </div>
    </div>
  );
}
