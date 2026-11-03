import { Check, Download, Store } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { AGENTS, useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Empty, Input } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { Credit } from "../protocol/domain";

// The Specialist hub: a catalog of skill repositories pinned to a commit (the hub's
// .pengupool/catalog.json). Install clones the helper at its pin into the Skill library.

interface Entry {
  name: string;
  description?: string;
  capabilities?: string[];
  harness?: string;
  category?: string;
  credits?: Credit[];
  requires?: string[];
  repo: string;
  ref?: string;
  commit: string;
  skill_id?: string;
  installed_commit?: string;
}

interface MarketView {
  url: string;
  default: string;
  name: string;
  description?: string;
  error?: string;
  skipped?: number;
  skills: Entry[];
}

const short = (sha?: string): string => (sha ?? "").slice(0, 7);

/** "Adapted from …" under a helper's description; nothing when it has no credits. Only http(s) links open. */
export function Credits({ credits, className }: { credits?: Credit[]; className?: string }): ReactElement | null {
  const cs = (credits ?? []).filter((c) => c.name !== "");
  if (cs.length === 0) return null;
  return (
    <span className={`block text-caption text-faint ${className ?? ""}`}>
      Adapted from{" "}
      {cs.map((c, i) => (
        <span key={i}>
          {i > 0 ? ", " : null}
          {/^https?:\/\//.test(c.url) ? (
            <a className="underline hover:text-muted-foreground" href={c.url} target="_blank" rel="noreferrer">
              {c.name}
            </a>
          ) : (
            c.name
          )}
        </span>
      ))}
    </span>
  );
}

export function useMarketplace(rev = 0): MarketView | null {
  const app = useApp();
  const [view, setView] = useState<MarketView | null>(null);
  useEffect(() => {
    void app.call<MarketView>("marketplace.list", {}).then((r) => setView(r ?? null));
  }, [rev, app.domain.skills.length]);
  return view;
}

export function MarketplaceSheet(): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const [rev, setRev] = useState(0);
  const m = useMarketplace(rev);
  const [busy, setBusy] = useState<string | null>(null);
  const run = async (key: string, fn: () => Promise<unknown>): Promise<void> => {
    setBusy(key);
    await fn();
    setBusy(null);
    setRev((n) => n + 1);
  };
  return (
    <Sheet title="Specialist hub" description="Specialists from the hub's catalog, each pinned to a commit. Install one, then put it on a team." onClose={close} wide>
      <div className="grid grid-cols-1 gap-3">
        {m === null ? <p className="text-label text-faint">Loading…</p> : null}
        {m !== null ? (
          <>
            <header className="flex flex-wrap items-center gap-2">
              <div className="min-w-0 flex-1">
                <h3 className="text-body font-semibold">{m.name}</h3>
                {m.description !== undefined ? <p className="text-caption text-muted-foreground">{m.description}</p> : null}
              </div>
            </header>
            <RepoField url={m.url} fallback={m.default} onSaved={() => setRev((n) => n + 1)} />
            {m.error !== undefined ? <p className="rounded-md bg-danger/10 p-2 text-caption text-danger">{m.error}</p> : null}
            {(m.skipped ?? 0) > 0 ? (
              <p className="text-caption text-faint">
                {m.skipped} more {m.skipped === 1 ? "entry is" : "entries are"} bundled in the catalog or not pinned to a commit, so PenguPool cannot install {m.skipped === 1 ? "it" : "them"}.
              </p>
            ) : null}
            {m.error === undefined && m.skills.length === 0 ? (
              <Empty icon={<Store />} title="No specialists yet">
                The Specialist hub lists none yet.
              </Empty>
            ) : (
              <div className="divide-y divide-border rounded-lg border border-border bg-raised">
                {m.skills.map((e) => (
                  <EntryRow key={e.name} e={e} busy={busy} run={run} />
                ))}
              </div>
            )}
          </>
        ) : null}
      </div>
    </Sheet>
  );
}

// The hub's repository: the default hub unless the owner sets another; the catalog is fetched from it on refresh.
function RepoField({ url, fallback, onSaved }: { url: string; fallback: string; onSaved: () => void }): ReactElement {
  const app = useApp();
  const [repo, setRepo] = useState(url);
  useEffect(() => setRepo(url), [url]);
  const save = async (value: string): Promise<void> => {
    if ((await app.call("marketplace.set", { repo: value.trim() })) === undefined) return;
    await app.call("marketplace.refresh", {});
    toast.success("Hub repository saved");
    onSaved();
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Input className="min-w-0 flex-1 font-mono" value={repo} onChange={(e) => setRepo(e.target.value)} placeholder={fallback} aria-label="Hub repository" />
      <Button disabled={repo.trim() === url} onClick={() => void save(repo)}>
        Save
      </Button>
      <Button variant="ghost" disabled={url === fallback} onClick={() => void save("")}>
        Use default
      </Button>
    </div>
  );
}

function EntryRow({ e, busy, run }: { e: Entry; busy: string | null; run: (k: string, fn: () => Promise<unknown>) => Promise<void> }): ReactElement {
  const app = useApp();
  const [path, setPath] = useState("");
  const [asking, setAsking] = useState(false);
  const installed = e.skill_id !== undefined;
  const behind = installed && e.installed_commit !== undefined && e.installed_commit !== e.commit;
  const harness = AGENTS.find((a) => a.id === e.harness)?.label;
  return (
    <div className="grid grid-cols-1 gap-1.5 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-label font-semibold">{e.name}</span>
        <span className="font-mono text-micro text-faint" title={e.commit}>
          {e.ref !== undefined && e.ref !== "" ? `${e.ref} @ ` : "@ "}
          {short(e.commit)}
        </span>
        {harness !== undefined ? <span className="text-micro text-muted-foreground">runs best on {harness}</span> : null}
        {(e.requires ?? []).length > 0 ? (
          <span className="text-micro text-muted-foreground">only on {(e.requires ?? []).map((r) => AGENTS.find((a) => a.id === r)?.label ?? r).join(" or ")}</span>
        ) : null}
        <span className="flex-1" />
        {installed ? (
          <span className="flex items-center gap-1 text-caption text-success" title={behind ? `Your checkout is at ${short(e.installed_commit)}` : undefined}>
            <Check className="size-3.5" />
            {behind ? `Installed (at ${short(e.installed_commit)})` : "Installed"}
          </span>
        ) : asking ? null : (
          <Button size="sm" variant="default" disabled={busy !== null} onClick={() => setAsking(true)}>
            <Download /> Install
          </Button>
        )}
      </div>
      {e.description !== undefined ? <p className="text-label text-muted-foreground">{e.description}</p> : null}
      <Credits credits={e.credits} />
      {(e.capabilities ?? []).length > 0 ? <p className="text-caption text-faint">{(e.capabilities ?? []).join(" · ")}</p> : null}
      {asking ? (
        <form
          className="mt-1 flex gap-2"
          onSubmit={(ev) => {
            ev.preventDefault();
            void run(`i${e.name}`, async () => {
              const r = await app.call("marketplace.install", { name: e.name, path: path.trim() });
              if (r !== undefined) {
                toast.success(`Installed ${e.name} at ${short(e.commit)}`);
                setAsking(false);
              }
            });
          }}
        >
          <Input value={path} onChange={(ev) => setPath(ev.target.value)} placeholder="Folder (default: inside PenguPool's store)" aria-label="Clone folder" className="font-mono" />
          <Button type="button" variant="ghost" onClick={() => setAsking(false)}>
            Cancel
          </Button>
          <Button type="submit" variant="default" disabled={busy !== null}>
            {busy === `i${e.name}` ? "Cloning…" : "Clone and install"}
          </Button>
        </form>
      ) : null}
    </div>
  );
}
