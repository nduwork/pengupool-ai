import { ArrowUpToLine, ChevronDown, ChevronRight, Copy, Eraser, ExternalLink, FolderOpen, FoldVertical, History, IdCard, Pencil, Plus, RotateCcw, SquareTerminal, Users, X } from "lucide-react";
import { ContextMenu } from "radix-ui";
import { useEffect, useState, type ReactElement, type ReactNode } from "react";
import { toast } from "sonner";

import { AGENTS, ago, harnessLabel, isAgent, teamOf, useApp } from "../../app/ctx";
import { localDesktop, onSSHHost } from "../../app/desktop";
import { Button } from "../../components/ui/button";
import { Badge, Input, Label, Textarea } from "../../components/ui/controls";
import { Sheet } from "../../components/ui/dialog";
import type { SessionView } from "../../protocol/control";
import type { Team } from "../../protocol/domain";
import { cn } from "../../lib/utils";
import { NewSessionDialog } from "../NewSession";

// The session actions v1 offered on a card's right-click menu, and the background menu.

const itemCls = "flex h-7 cursor-default select-none items-center gap-2 rounded-md px-2 text-label outline-none data-[disabled]:opacity-40 data-[highlighted]:bg-hover";
const sepCls = "my-1 h-px bg-border";

export function MenuContent({ children }: { children: ReactNode }): ReactElement {
  return (
    <ContextMenu.Portal>
      <ContextMenu.Content className="z-50 min-w-56 rounded-lg border border-border bg-raised p-1 shadow-2xl">{children}</ContextMenu.Content>
    </ContextMenu.Portal>
  );
}

/** A menu row: an icon column (as v1's VS Code menus, kept even when empty so labels line up), then the label. */
export function Item({ onSelect, children, disabled, danger, icon }: { onSelect: () => void; children: ReactNode; disabled?: boolean; danger?: boolean; icon?: ReactElement }): ReactElement {
  return (
    <ContextMenu.Item className={itemCls + (danger === true ? " text-danger" : "")} onSelect={onSelect} disabled={disabled}>
      <span className={cn("grid size-4 shrink-0 place-items-center [&_svg]:size-3.5", danger === true ? "text-danger" : "text-muted-foreground")}>{icon}</span>
      {children}
    </ContextMenu.Item>
  );
}

export function Sep(): ReactElement {
  return <ContextMenu.Separator className={sepCls} />;
}

/** The card menu: v1's session commands, mapped onto the host's actions. */
export function SessionMenu({ s, folded, canFold, onFold }: { s: SessionView; folded: boolean; canFold: boolean; onFold: (v: boolean) => void }): ReactElement {
  const app = useApp();
  const agent = isAgent(s);
  const running = s.state === "running";
  const team = teamOf(app.domain, s.id);
  const main = team?.members.find((m) => m.session_id === s.id && m.state === "active")?.role === "main";
  const show = (d: ReactElement): void => app.showDialog(d);
  const close = (): void => app.showDialog(null);
  return (
    <MenuContent>
      <Item icon={<SquareTerminal />} onSelect={() => app.select(s.id)}>
        Open / focus session
      </Item>
      <Sep />
      <Item icon={<Plus />} disabled={!agent} onSelect={() => show(<NewSessionDialog parentId={s.id} onClose={close} />)}>
        New child session…
      </Item>
      <Item icon={<History />} disabled={!agent} onSelect={() => show(<PreviousSheet parentId={s.id} />)}>
        Add previous session as child…
      </Item>
      <Sep />
      <Item icon={<FolderOpen />} onSelect={() => localDesktop?.revealPath(s.cwd)} disabled={localDesktop === undefined}>
        Reveal session folder
      </Item>
      <Item
        icon={<Copy />}
        onSelect={() => {
          void navigator.clipboard.writeText(s.cwd);
          toast.success("Path copied");
        }}
      >
        Copy path
      </Item>
      <Item
        icon={<SquareTerminal />}
        onSelect={() =>
          void app.call<SessionView>("shell.open", { cwd: s.cwd }).then((t) => {
            if (t !== undefined) app.select(t.id);
          })
        }
      >
        New terminal in this folder
      </Item>
      <Item icon={<ExternalLink />} onSelect={() => void app.call("session.open_terminal", { session_id: s.id })} disabled={!running || onSSHHost}>
        Open in Terminal app
      </Item>
      <Sep />
      <Item disabled={!agent || !running} onSelect={() => void app.call("session.slash", { session_id: s.id, command: "compact" })} icon={<FoldVertical />}>
        Compact context (/compact)
      </Item>
      <Item disabled={!agent || !running} onSelect={() => void app.call("session.slash", { session_id: s.id, command: "clear" })} icon={<Eraser />}>
        Fresh context ({AGENTS.find((a) => a.id === s.harness)?.clear ?? "/clear"})
      </Item>
      <Item icon={<RotateCcw />} disabled={!agent} onSelect={() => void app.call("session.restart", { session_id: s.id })}>
        Restart & resume
      </Item>
      <Sep />
      <Item icon={<Pencil />} onSelect={() => show(<RenameSheet s={s} />)}>
        Rename…
      </Item>
      <Item icon={<IdCard />} onSelect={() => show(<DescribeSheet s={s} />)}>
        Describe role…
      </Item>
      {team !== undefined ? (
        <Item icon={<Users />} onSelect={() => show(<RenameSheet team={team} />)}>
          Rename group {team.name}…
        </Item>
      ) : null}
      {team !== undefined && !main ? (
        <Item icon={<ArrowUpToLine />} onSelect={() => void app.call("team.group", { session_id: s.id })}>
          Split off as its own group, with its helpers (leave {team.name})
        </Item>
      ) : null}
      {team !== undefined && !main ? (
        <Item icon={<ArrowUpToLine />} onSelect={() => void app.call("team.group", { session_id: s.id, ungroup: true })}>
          Remove from {team.name} (keeps running, ungrouped)
        </Item>
      ) : null}
      {canFold ? (
        <Item icon={folded ? <ChevronRight /> : <ChevronDown />} onSelect={() => onFold(!folded)}>
          {folded ? "Unfold group" : "Fold group"}
        </Item>
      ) : null}
      <Sep />
      <Item icon={<Eraser />} onSelect={() => show(<DeleteRunLogSheet s={s} />)}>
        Delete run log…
      </Item>
      <Item danger icon={<X />} onSelect={() => show(<CloseSheet s={s} />)}>
        Close…
      </Item>
    </MenuContent>
  );
}

/** Renames a session, or a group when `team` is given. */
export function RenameSheet({ s, team }: { s?: SessionView; team?: Team }): ReactElement {
  const app = useApp();
  const current = team?.name ?? s?.name ?? "";
  const [name, setName] = useState(current);
  const close = (): void => app.showDialog(null);
  const save = async (): Promise<void> => {
    const r = team !== undefined ? await app.call("team.rename", { team_id: team.id, name }) : await app.call("session.rename", { session_id: s?.id, name });
    if (r !== undefined) close();
  };
  return (
    <Sheet
      title={`Rename ${team !== undefined ? "group " : ""}${current}`}
      description={team !== undefined ? "The name shown in the sidebar and the map." : "Teammates address members by name."}
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="default" disabled={name.trim() === ""} onClick={() => void save()}>
            Rename
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </form>
    </Sheet>
  );
}

export function DescribeSheet({ s }: { s: SessionView }): ReactElement {
  const app = useApp();
  const [text, setText] = useState(s.summary ?? "");
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title={`Describe ${s.name}'s role`}
      description="Shown on the map and given to the agent with its team context."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant="default"
            onClick={async () => {
              if ((await app.call("session.describe", { session_id: s.id, summary: text })) !== undefined) close();
            }}
          >
            Save
          </Button>
        </>
      }
    >
      <Label hint="One or two lines: what this session owns.">
        Role
        <Textarea value={text} onChange={(e) => setText(e.target.value)} autoFocus placeholder="Owns the release pipeline and its checks" />
      </Label>
    </Sheet>
  );
}

/** The run log is the host's disposable record of what the agent did; the owner may purge it. */
export function DeleteRunLogSheet({ s }: { s: SessionView }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title={`Delete the run log of ${s.name}?`}
      description="Removes the file the host keeps of this session's activity. The session, its transcript and the team's record are not touched."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={async () => {
              if ((await app.call("runlog.delete", { session_id: s.id })) !== undefined) {
                toast.success("Run log deleted");
                close();
              }
            }}
          >
            Delete run log
          </Button>
        </>
      }
    >
      <span />
    </Sheet>
  );
}

export function CloseSheet({ s }: { s: SessionView }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const team = teamOf(app.domain, s.id);
  const helper = team?.members.find((m) => m.session_id === s.id && m.worktree !== undefined && m.role !== "main");
  return (
    <Sheet
      title={`Close ${s.name}?`}
      description={
        helper !== undefined
          ? "Stops the helper and takes it off its team. Its worktree is removed when nothing would be lost, otherwise it stays on disk."
          : "Stops the agent and removes the session from the map. Its transcript stays, so you can add it back later."
      }
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={async () => {
              if ((await app.call("session.close", { session_id: s.id })) !== undefined) close();
            }}
          >
            Close session
          </Button>
        </>
      }
    >
      <p className="text-label text-muted-foreground">
        {harnessLabel(s.harness)} · {s.cwd}
      </p>
    </Sheet>
  );
}

interface Previous {
  harness: string;
  id: string;
  transcript: string;
  cwd: string;
  title: string;
  modified: string;
  live: boolean;
}

/** Resume past Claude / OMP conversations as sessions (v1's Add Previous and Resume Previous). */
export function PreviousSheet({ parentId }: { parentId?: string }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const parent = app.sessions.find((s) => s.id === parentId);
  const [list, setList] = useState<Previous[] | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void app.call<Previous[] | null>("session.previous", { limit: 120 }).then((r) => setList(r ?? []));
  }, []);
  const shown = (list ?? []).filter((p) => filter === "" || `${p.title} ${p.cwd}`.toLowerCase().includes(filter.toLowerCase()));
  const resume = async (): Promise<void> => {
    setBusy(true);
    for (const p of shown.filter((x) => picked.has(x.id))) {
      await app.call("session.adopt", { harness: p.harness, id: p.id, transcript: p.transcript, cwd: p.cwd, parent_id: parentId });
    }
    setBusy(false);
    close();
  };
  return (
    <Sheet
      title={parent !== undefined ? `Add a previous session under ${parent.name}` : "Resume previous sessions"}
      description="Past Claude Code and OMP conversations, newest first. Each comes back as a session with its history."
      onClose={close}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="default" disabled={busy || picked.size === 0} onClick={() => void resume()}>
            {busy ? "Resuming…" : `Resume ${picked.size || ""}`}
          </Button>
        </>
      }
    >
      <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter by prompt or folder" className="mb-3" autoFocus />
      {list === null ? <p className="text-label text-faint">Looking for transcripts…</p> : null}
      {list !== null && shown.length === 0 ? <p className="text-label text-muted-foreground">No resumable conversations found.</p> : null}
      <div className="grid grid-cols-1 gap-1">
        {shown.map((p) => (
          <label key={p.id} className="flex items-start gap-3 rounded-md p-2 hover:bg-hover">
            <input
              type="checkbox"
              className="mt-1"
              disabled={p.live}
              checked={picked.has(p.id)}
              onChange={(e) => {
                const n = new Set(picked);
                if (e.target.checked) n.add(p.id);
                else n.delete(p.id);
                setPicked(n);
              }}
            />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-label">{p.title || "(no prompt yet)"}</span>
              <span className="block truncate font-mono text-micro text-faint">
                {p.cwd} · {ago(p.modified)}
              </span>
            </span>
            <Badge tone={p.harness === "claude" ? "primary" : "info"}>{harnessLabel(p.harness)}</Badge>
            {p.live ? <Badge tone="success">running</Badge> : null}
          </label>
        ))}
      </div>
    </Sheet>
  );
}
