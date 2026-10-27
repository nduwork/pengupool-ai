import { BookOpen, ChevronDown, ChevronRight, Pencil, Plus, Settings as SettingsIcon, SquareTerminal, Users } from "lucide-react";
import { useState, type ReactElement } from "react";
import { Group, Panel, useDefaultLayout, usePanelRef } from "react-resizable-panels";

import { StatusDot, harnessLabel, useApp, usePersisted, type Nav } from "../app/ctx";
import { desktop } from "../app/desktop";
import { Kbd, SectionTitle, Tip } from "../components/ui/controls";
import { cn } from "../lib/utils";
import type { SessionView } from "../protocol/control";
import type { Team } from "../protocol/domain";
import { EnvSwitcher } from "./EnvSwitcher";
import { Explorer } from "./Explorer";
import { GettingStarted } from "./GettingStarted";
import { RenameSheet } from "./map/actions";
import { NewSessionDialog } from "./NewSession";
import { Handle } from "./Page";

const NAV: Array<{ id: Nav; label: string; icon: ReactElement }> = [
  { id: "workspace", label: "Workspace", icon: <SquareTerminal /> },
  { id: "skills", label: "Helper agents", icon: <BookOpen /> },
  { id: "teams", label: "Teams", icon: <Users /> },
  { id: "settings", label: "Settings", icon: <SettingsIcon /> },
];

type SessionGroup = { key: string; name: string; title: string; team?: Team; rows: Array<{ s: SessionView; depth: number }> };

/** Session groups: each active team with its members in tree order (lead first), then everything ungrouped. */
function groupSessions(app: ReturnType<typeof useApp>): SessionGroup[] {
  const byId = new Map(app.sessions.map((s) => [s.id, s]));
  const placed = new Set<string>();
  const out: SessionGroup[] = [];
  for (const t of app.domain.teams.filter((x) => x.state === "active")) {
    const members = t.members.filter((m) => m.state === "active" && byId.has(m.session_id));
    if (members.length === 0) continue;
    const ids = new Set(members.map((m) => m.session_id));
    const main = members.find((m) => m.role === "main") ?? members[0];
    const kids = new Map<string, string[]>();
    const roots: string[] = [];
    for (const m of members) {
      const parent = m.parent !== undefined && ids.has(m.parent) ? m.parent : m === main ? "" : main.session_id;
      if (parent === "") roots.push(m.session_id);
      else kids.set(parent, [...(kids.get(parent) ?? []), m.session_id]);
    }
    const rows: SessionGroup["rows"] = [];
    const walk = (id: string, depth: number): void => {
      if (placed.has(id)) return;
      placed.add(id);
      rows.push({ s: byId.get(id) as SessionView, depth });
      for (const c of kids.get(id) ?? []) walk(c, depth + 1);
    };
    for (const r of roots) walk(r, 0);
    out.push({ key: t.id, name: t.name, title: t.goal ?? t.name, team: t, rows });
  }
  const rest = app.sessions.filter((s) => !placed.has(s.id));
  if (rest.length > 0) out.push({ key: "ungrouped", name: "Ungrouped", title: "Sessions not in a group", rows: rest.map((s) => ({ s, depth: 0 })) });
  return out;
}

export function Sidebar(): ReactElement {
  const app = useApp();
  const [folded, setFolded] = usePersisted<Record<string, boolean>>("sidebar.folded", {});
  const [explorerOpen, setExplorerOpen] = useState(true);
  const explorer = usePanelRef();
  const split = useDefaultLayout({ id: "pengupool.layout.sidebar", storage: localStorage });
  const selected = app.sessions.find((s) => s.id === app.selected);
  const pending = app.domain.approvals.filter((a) => a.state === "pending").length;
  const running = app.sessions.filter((s) => s.state === "running").length;
  const newSession = (): void => app.showDialog(<NewSessionDialog onClose={() => app.showDialog(null)} />);
  const groups = groupSessions(app);

  return (
    <aside className="flex h-full min-h-0 flex-col bg-sidebar" aria-label="Sidebar">
      {/* Title-bar row: room for the macOS traffic lights, draggable. */}
      <div className={cn("drag flex h-11 shrink-0 items-center gap-2 px-3", desktop?.platform === "darwin" && "pl-[78px]")}>
        <EnvSwitcher />
      </div>

      <div className="px-2 pb-2">
        <button type="button" onClick={newSession} className="flex h-8 w-full items-center gap-2 rounded-md border border-border bg-panel px-2.5 text-label font-medium shadow-sm hover:bg-hover">
          <Plus className="size-4 text-primary" />
          New session
          <span className="ml-auto">
            <Kbd>⌘N</Kbd>
          </span>
        </button>
      </div>

      <nav className="grid grid-cols-1 gap-px px-2" aria-label="Main">
        {NAV.map((n) => (
          <button
            key={n.id}
            type="button"
            onClick={() => app.setNav(n.id)}
            aria-current={app.nav === n.id}
            className={cn(
              "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-label text-muted-foreground hover:bg-hover hover:text-foreground [&_svg]:size-4",
              app.nav === n.id && "bg-selected font-medium text-foreground",
            )}
          >
            {n.icon}
            {n.label}
            {n.id === "workspace" && running > 0 ? <span className="ml-auto text-micro text-faint">{running}</span> : null}
            {n.id === "teams" && pending > 0 ? <span className="ml-auto rounded-full bg-warning px-1.5 text-micro font-semibold text-black">{pending}</span> : null}
          </button>
        ))}
      </nav>

      <Group orientation="vertical" className="mt-2 min-h-0 flex-1" defaultLayout={split.defaultLayout} onLayoutChanged={split.onLayoutChanged}>
        <Panel id="groups" minSize={80}>
          <div className="h-full overflow-y-auto px-2">
            <SectionTitle>Groups</SectionTitle>
            {groups.length === 0 ? <p className="px-2 py-1 text-caption text-muted-foreground">No sessions yet.</p> : null}
            {app.sessions.length > 0 ? <GettingStarted /> : null}
            {groups.map((g) => (
              <div key={g.key} className="mb-1">
                <div className="group/g flex h-7 items-center rounded-md hover:bg-hover">
                  <button
                    type="button"
                    onClick={() => setFolded({ ...folded, [g.key]: !folded[g.key] })}
                    onDoubleClick={() => g.team !== undefined && app.showDialog(<RenameSheet team={g.team} />)}
                    className="flex h-full min-w-0 flex-1 items-center gap-1.5 px-1.5 text-label font-medium"
                    title={g.title}
                  >
                    {folded[g.key] === true ? <ChevronRight className="size-3.5 text-faint" /> : <ChevronDown className="size-3.5 text-faint" />}
                    <Users className="size-3.5 text-faint" />
                    <span className="truncate">{g.name}</span>
                    <span className="ml-auto text-micro text-faint">{g.rows.length}</span>
                  </button>
                  {g.team !== undefined ? (
                    <Tip label="Rename group">
                      <button
                        type="button"
                        aria-label={`Rename group ${g.name}`}
                        onClick={() => g.team !== undefined && app.showDialog(<RenameSheet team={g.team} />)}
                        className="mr-1 hidden rounded p-0.5 text-faint hover:text-foreground focus-visible:inline-flex group-hover/g:inline-flex"
                      >
                        <Pencil className="size-3" />
                      </button>
                    </Tip>
                  ) : null}
                </div>
                {folded[g.key] === true
                  ? null
                  : g.rows.map(({ s, depth }) => (
                      <button
                        key={s.id}
                        type="button"
                        onClick={() => app.select(s.id)}
                        aria-current={s.id === app.selected}
                        className={cn(
                          "group flex h-7 w-full items-center gap-2 rounded-md pr-1.5 text-label text-muted-foreground hover:bg-hover hover:text-foreground",
                          s.id === app.selected && "bg-selected text-foreground",
                        )}
                        style={{ paddingLeft: 28 + Math.min(depth, 4) * 12 }}
                      >
                        <StatusDot status={s.status} state={s.state} harness={s.harness} active_at={s.active_at} role={s.role} />
                        <span className="truncate">{s.name}</span>
                        <span className="ml-auto shrink-0 text-micro text-faint">{s.role === "helper" ? "helper" : s.context_pct !== undefined ? `${s.context_pct}%` : harnessLabel(s.harness)}</span>
                      </button>
                    ))}
              </div>
            ))}
          </div>
        </Panel>
        <Handle dir="y" />
        {/* Collapses to its title row, like a VS Code side bar view; drag the sash to resize. */}
        <Panel id="explorer" panelRef={explorer} collapsible collapsedSize={30} minSize={120} defaultSize="44" onResize={(sz) => setExplorerOpen(sz.inPixels > 31)}>
          <div className="flex h-full min-h-0 flex-col">
            <SectionTitle
              action={
                <Tip label={explorerOpen ? "Collapse Explorer" : "Expand Explorer"}>
                  <button
                    type="button"
                    className="rounded p-0.5 text-faint hover:bg-hover"
                    onClick={() => (explorer.current?.isCollapsed() === true ? explorer.current.expand() : explorer.current?.collapse())}
                  >
                    {explorerOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                  </button>
                </Tip>
              }
            >
              Explorer
            </SectionTitle>
            {explorerOpen ? <Explorer session={selected} /> : null}
          </div>
        </Panel>
      </Group>
    </aside>
  );
}
