import { Archive, BookOpen, FoldVertical, History, UnfoldVertical, Layers, ListTree, Minus, Pencil, Plus, RefreshCw, Rocket, Scan, Settings2, Unplug, Users, X } from "lucide-react";
import { ContextMenu } from "radix-ui";
import { toast } from "sonner";
import { useEffect, useMemo, useState, type ReactElement } from "react";

import { URGENCY, agentLabel, ctxClass, isShell, shownStatus, skillName, statusStyle, teamOf, useApp, usePersisted } from "../../app/ctx";
import { Button } from "../../components/ui/button";
import { Badge, Empty, RoleTag, Select, Tip, type Role } from "../../components/ui/controls";
import { cn } from "../../lib/utils";
import type { SessionView } from "../../protocol/control";
import type { Team, TeamMessage, Triage } from "../../protocol/domain";
import { ProposalSheet } from "../Approvals";
import { FormationSheet, leadOf, roleOf } from "../Formation";
import { SessionHover, UnreadDot } from "./SessionBits";
import { NewSessionDialog } from "../NewSession";
import { PATH_MIME, UNSAFE_NOTICE, typePath } from "../explorerPaths";
import { Item, MenuContent, PreviousSheet, RenameSheet, SessionMenu, Sep } from "./actions";
import { olderThan, type RecordedMessage } from "./olderMessages";
import { STATE_CELL, TYPE_CHIP, age, askPill, newestByPair, openAsks } from "./asks";
import { layout, rounded, type Block, type LayoutKind, type Route, type TreeNode } from "./layout";
import { AgentPool, DND } from "./Pool";
import { ClosedTeamsSheet, CloseTeamSheet, TeamSettingsSheet } from "./TeamSheets";

// The live map, as v1 drew it: each team laid out as a tree in its own frame (Org chart, Outline or
// Tree →), right-angled lines that light green when a parent messages a child and blue for
// the reply, ungrouped sessions stacked in a column on the right, folding to a lead, drag a card onto
// another to group it there or onto the canvas to ungroup, a right-click menu on every card and on
// the background, and the Skill agent pool drawer. Layout depends only on the topology and
// the options, so status updates restyle cards in place and never move them.

const HOT_MS = 90_000;
const DIR_COLOR = { down: "var(--route-down)", up: "var(--route-up)" }; // a message to a child, a reply to its parent
const LAYOUTS: Record<LayoutKind, string> = { org: "Org chart", outline: "Outline", right: "Tree →" };

function sizes(roomy: boolean): { S: { sib: number; rank: number; stack: number; indent: number }; pad: number } {
  return roomy ? { S: { sib: 28, rank: 56, stack: 12, indent: 28 }, pad: 20 } : { S: { sib: 12, rank: 36, stack: 8, indent: 22 }, pad: 12 };
}

// v1's two card widths: a card carrying a workflow chain is wider; heights fit the lines.
const CARD_W = 180;
const CHAIN_W = 260;
const CARD_H = 60;

interface Group {
  team: Team;
  roots: TreeNode[];
  hidden: Record<string, string[]>;
  block: Block;
}

/** The map. */
export function TeamMap(): ReactElement {
  const app = useApp();
  const { domain, sessions, selected } = app;
  const [kind, setKind] = usePersisted<LayoutKind>("map.layout", "org");
  const [roomy, setRoomy] = usePersisted("map.roomy", true);
  const [folded, setFoldedState] = usePersisted<string[]>("map.foldedIds", []);
  const [zoom, setZoom] = usePersisted("map.zoom", 1);
  const [drawer, setDrawer] = usePersisted("map.drawer", false);
  const [drawerW, setDrawerW] = usePersisted("map.drawerW", 260);
  const [flashIds, setFlash] = useState<string[]>([]);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const chains = useChains();

  const foldedSet = useMemo(() => new Set(folded), [folded]);
  const setFold = (id: string, on: boolean): void => setFoldedState(on ? [...new Set([...folded, id])] : folded.filter((x) => x !== id));
  const byId = useMemo(() => new Map(sessions.map((s) => [s.id, s])), [sessions]);
  const { S, pad } = sizes(roomy);
  // Each card sizes to its content, as v1 measured it: roomy cards carry their workflow chain
  // (compact leaves it to the tooltip), so only a card with a chain grows.
  const sizeOf = (id: string): { w: number; h: number } => {
    const c = roomy ? chains[id] : undefined;
    const w = c === undefined ? CARD_W : CHAIN_W;
    const name = Math.min(2, linesAt(byId.get(id)?.name ?? "", w - CARD_INSET - NAME_PAD, "name"));
    return { w, h: CARD_H + (name - 1) * 14 + (c === undefined ? 0 : linesAt(chainText(c), w - CARD_INSET, "chain") * 14) };
  };
  const sizeKey = sessions.map((x) => `${x.id}:${sizeOf(x.id).w}x${sizeOf(x.id).h}`).join();

  // Each active team becomes a forest of its members (parent links); folded nodes keep their card.
  const groups: Group[] = useMemo(() => {
    const out: Group[] = [];
    for (const team of domain.teams.filter((t) => t.state === "active")) {
      const members = team.members.filter((m) => m.state === "active");
      if (members.length === 0) continue;
      const ids = new Set(members.map((m) => m.session_id));
      const main = members.find((m) => m.role === "main") ?? members[0];
      const kids = new Map<string, string[]>();
      const roots: string[] = [];
      for (const m of members) {
        const parent = m.parent !== undefined && m.parent !== "" && ids.has(m.parent) ? m.parent : m === main ? "" : main.session_id;
        if (parent === "") roots.push(m.session_id);
        else kids.set(parent, [...(kids.get(parent) ?? []), m.session_id]);
      }
      const hidden: Record<string, string[]> = {};
      const below = (id: string): string[] => (kids.get(id) ?? []).flatMap((c) => [c, ...below(c)]);
      const build = (id: string): TreeNode => {
        if (foldedSet.has(id) && (kids.get(id) ?? []).length > 0) {
          hidden[id] = below(id);
          return { id, children: [] };
        }
        return { id, children: (kids.get(id) ?? []).map(build) };
      };
      const forest = roots.map(build);
      out.push({ team, roots: forest, hidden, block: layout(kind, forest, sizeOf, S) });
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.teams, sessions.length, kind, roomy, folded, sizeKey]);

  const inTeam = new Set(domain.teams.filter((t) => t.state === "active").flatMap((t) => t.members.filter((m) => m.state === "active").map((m) => m.session_id)));
  const ungrouped = sessions.filter((s) => !inTeam.has(s.id) && !isShell(s));
  const parentOf = (teamId: string, id: string): string | undefined => domain.teams.find((t) => t.id === teamId)?.members.find((m) => m.session_id === id)?.parent;

  // Lit edges: the latest message on a tree line within 90 s that is no longer queued, green down,
  // blue up; a queued one leaves its line plain grey (the message log says it is queued). The 90 s count
  // from its delivery, so one that waited in the queue still arrives with beads.
  const hot = new Map<string, "down" | "up">();
  const now = Date.now();
  for (const m of domain.messages) {
    if (m.state === "queued" || now - Date.parse(m.delivered_at ?? m.at) > HOT_MS) continue;
    const team = domain.teams.find((t) => t.id === m.team_id);
    if (team === undefined) continue;
    if (parentOf(team.id, m.to) === m.from) hot.set(`${m.from}>${m.to}`, "down");
    else if (parentOf(team.id, m.from) === m.to) hot.set(`${m.to}>${m.from}`, "up");
  }

  // Open asks: a pill on the asker's card and on the route between asker and recipient.
  const asks = openAsks(domain.messages);
  const asksOn = (a: string, b: string): TeamMessage[] => asks.filter((x) => (x.from === a && x.to === b) || (x.from === b && x.to === a));
  const newest = newestByPair(domain.messages.filter((m) => m.state !== "queued")); // orders the lit routes
  // The lit routes, a message arrived within 90 s (beads), oldest message first so the newest draws
  // on top where routes share a trunk.
  const lit = (routes: Route[]): Array<{ r: Route; dir: "down" | "up"; at: number }> =>
    routes
      .flatMap((r) => {
        const dir = hot.get(`${r.from}>${r.to}`);
        const at = Math.max(...[newest.get(`${r.from}>${r.to}`), newest.get(`${r.to}>${r.from}`)].map((m) => (m === undefined ? 0 : Date.parse(m.delivered_at ?? m.at))));
        return dir === undefined ? [] : [{ r, dir, at }];
      })
      .sort((a, b) => a.at - b.at);

  const pendingLaunch = domain.approvals.filter((a) => a.kind === "launch" && a.state === "pending");
  const flash = (ids: string[]): void => {
    setFlash(ids);
    window.setTimeout(() => setFlash([]), 1600);
  };
  const allFoldable = groups.flatMap((g) => g.team.members.filter((m) => g.team.members.some((x) => x.parent === m.session_id) || m.role === "main").map((m) => m.session_id));
  const anyUnfolded = allFoldable.some((id) => !foldedSet.has(id));

  // Canvas drops: a card dropped on empty canvas leaves its team; a repo starts a session there.
  const canvasDrop = (e: React.DragEvent): void => {
    if ((e.target as HTMLElement).closest("[data-card]") !== null) return;
    const sid = e.dataTransfer.getData(DND.session);
    const repo = e.dataTransfer.getData(DND.repo);
    const skill = e.dataTransfer.getData(DND.skill);
    if (sid !== "") void app.call("team.group", { session_id: sid });
    else if (repo !== "") app.showDialog(<NewSessionDialog initialPath={repo} onClose={() => app.showDialog(null)} />);
    else if (skill !== "") {
      const sk = domain.skills.find((x) => x.id === skill);
      if (sk !== undefined) app.showDialog(<NewSessionDialog initialPath={sk.path} onClose={() => app.showDialog(null)} />);
    }
  };

  // A session dropped on a group's frame or title joins that group under its LEAD; dropped on the
  // Ungrouped column it leaves its group and keeps running. Both stop here, so the canvas drop (which
  // splits a session off as a group of its own) does not also run.
  const frameDrop = (e: React.DragEvent, team: Team | undefined): void => {
    const sid = e.dataTransfer.getData(DND.session);
    if (sid === "") return;
    e.preventDefault();
    e.stopPropagation();
    if (team === undefined) void app.call("team.group", { session_id: sid, ungroup: true });
    else {
      const lead = leadOf(team);
      if (lead !== "" && lead !== sid) void app.call("team.group", { session_id: sid, parent_id: lead });
    }
  };
  const frameOver = (e: React.DragEvent): void => {
    if (e.dataTransfer.types.includes(DND.session)) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  // Card drops: group a session under this card, start a child session from a repo, or add a skill
  // agent to this card's team as a helper.
  const cardDrop = async (e: React.DragEvent, target: SessionView): Promise<void> => {
    e.preventDefault();
    e.stopPropagation();
    setDropTarget(null);
    const path = e.dataTransfer.getData(PATH_MIME);
    if (path !== "") {
      // An Explorer row: its path is typed into this session's prompt, as on its terminal.
      if (target.state === "running") {
        if (typePath(target.id, path)) app.select(target.id);
        else toast.error(UNSAFE_NOTICE);
      }
      return;
    }
    const sid = e.dataTransfer.getData(DND.session);
    const repo = e.dataTransfer.getData(DND.repo);
    const skill = e.dataTransfer.getData(DND.skill);
    if (sid !== "" && sid !== target.id) await app.call("team.group", { session_id: sid, parent_id: target.id });
    else if (repo !== "") app.showDialog(<NewSessionDialog initialPath={repo} parentId={target.id} onClose={() => app.showDialog(null)} />);
    else if (skill !== "") {
      let team = teamOf(domain, target.id);
      if (team === undefined) {
        const t = await app.call<Team>("team.create", { name: target.name, session_ids: [target.id] });
        if (t === undefined) return;
        team = t;
      }
      // Adding a member is a formation change: the owner approves it before the agent starts.
      app.showDialog(<FormationSheet team={team} change={{ op: "add", skill_id: skill, parent: target.id }} />);
    }
  };

  const card = (s: SessionView, box: { x: number; y: number; w: number; h: number }, opts: { teamId?: string; hidden?: string[]; hasKids: boolean; lone?: boolean }): ReactElement => {
    const st = shownStatus(s, app.gates, domain.approvals);
    const style = statusStyle(st);
    const team = opts.teamId !== undefined ? domain.teams.find((t) => t.id === opts.teamId) : undefined;
    const member = team?.members.find((m) => m.session_id === s.id);
    const isFolded = foldedSet.has(s.id) && (opts.hidden ?? []).length > 0;
    const urgent = (opts.hidden ?? [])
      .map((id) => byId.get(id))
      .filter((x): x is SessionView => x !== undefined)
      .map((x) => shownStatus(x, app.gates, domain.approvals))
      .sort((a, b) => (URGENCY[b] ?? 0) - (URGENCY[a] ?? 0))[0];
    const repoName = s.cwd.split("/").filter(Boolean).pop() ?? "";
    const mine = asks.filter((a) => a.from === s.id);
    const askName = (id: string): string => byId.get(id)?.name ?? id.slice(0, 8);
    return (
      <ContextMenu.Root key={s.id}>
        <Tip side="right" label={<SessionHover s={s} />}>
        <ContextMenu.Trigger asChild>
          <div
            data-card={s.id}
            role="button"
            tabIndex={0}
            draggable
            aria-current={selected === s.id}
            aria-label={`${s.name}, ${style.label}${s.hooks_missing === true ? ", PenguPool's hooks aren't reaching it" : ""}${mine.length > 0 ? `, ${askLabel(mine, askName)}` : ""}`}
            onClick={() => app.select(s.id)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                app.select(s.id);
              }
            }}
            onDragStart={(e) => {
              e.dataTransfer.setData(DND.session, s.id);
              e.dataTransfer.effectAllowed = "move";
            }}
            onDragOver={(e) => {
              if ([DND.session, DND.repo, DND.skill, PATH_MIME].some((t) => e.dataTransfer.types.includes(t))) {
                e.preventDefault();
                e.stopPropagation();
                setDropTarget(s.id);
              }
            }}
            onDragLeave={() => setDropTarget((d) => (d === s.id ? null : d))}
            onDrop={(e) => void cardDrop(e, s)}
            className={cn(
              "group absolute flex cursor-default flex-col justify-center rounded-md border-[1.5px] bg-panel px-2.5 py-1.5 text-left text-[10px] leading-[14px] outline-none transition-[border-color,border-width]",
              "hover:border-2 focus-visible:ring-2 focus-visible:ring-ring",
              opts.lone === true && "border-dashed",
              st === "approval" && "border-2", // waiting on the owner: a red outline, thicker so it reads in both themes
              selected === s.id && "ring-2 ring-primary ring-offset-2 ring-offset-background",
              dropTarget === s.id && "ring-2 ring-primary",
              flashIds.includes(s.id) && "ring-4 ring-success",
              isFolded && "shadow-[4px_4px_0_-1.5px_var(--panel),4px_4px_0_0_var(--border)]",
            )}
            style={{ left: box.x, top: box.y, width: box.w, height: box.h, borderColor: tone(st) }}
          >
            {/* As v1 drew it: the state in its colour, the name, then harness · context · repo, then the step in hand. */}
            {(() => {
              const role = team !== undefined ? roleOf(domain, app.sessions, s.id) : undefined;
              return role !== undefined ? (
                <RoleTag role={role} className="absolute right-2 top-1.5 rounded-[2px] px-[3px] text-[8px] leading-[11px] tracking-[0.08em] uppercase" />
              ) : null;
            })()}
            {mine.length > 0 ? <AskPill asks={mine} name={askName} className="absolute bottom-1.5 right-2" /> : null}
            <span className="flex items-center gap-1.5 font-semibold" style={{ color: tone(st) }}>
              {SYMBOL[st] !== undefined ? `${SYMBOL[st]} ` : ""}
              {style.label}
              <UnreadDot id={s.id} />
            </span>
            <span className="line-clamp-2 pr-8 text-[12px] font-semibold text-foreground [overflow-wrap:anywhere]">{s.name}</span>
            <span className={cn("truncate text-muted-foreground", mine.length > 1 ? "pr-[4.5rem]" : mine.length > 0 && "pr-10")} title={agentLabel(s)}>
              {agentLabel(s)}
              {s.hooks_missing === true ? (
                <span className="ml-1 inline-flex items-center gap-0.5 align-bottom text-foreground">
                  <Unplug className="size-2.5 shrink-0" aria-hidden />
                  no hooks
                </span>
              ) : null}
              {s.context_pct !== undefined ? (
                <>
                  {" · "}
                  <span className={ctxClass(s.context_pct)}>{s.context_pct}%</span>
                </>
              ) : null}
              {repoName !== "" ? ` · ${repoName}` : ""}
              {member !== undefined && member.role !== "main" && member.skill_id !== undefined ? ` · ${skillName(domain, member.skill_id)}` : ""}
            </span>
            {roomy && chains[s.id] !== undefined ? <StepLine chain={chains[s.id]} /> : null}
            {opts.hasKids || isFolded ? (
              <button
                type="button"
                draggable={false}
                onClick={(e) => {
                  e.stopPropagation();
                  setFold(s.id, !foldedSet.has(s.id));
                }}
                className={cn(
                  "absolute -bottom-2.5 left-1/2 flex h-5 min-w-5 -translate-x-1/2 items-center justify-center rounded-full border border-border bg-raised px-1.5 text-[10px] font-medium text-muted-foreground shadow-sm hover:text-foreground",
                  !isFolded && "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
                  isFolded && urgent !== undefined && statusStyle(urgent).text,
                )}
                aria-label={isFolded ? `Unfold ${(opts.hidden ?? []).length} sessions` : "Fold children"}
              >
                {isFolded ? `+${(opts.hidden ?? []).length}` : "−"}
              </button>
            ) : null}
          </div>
        </ContextMenu.Trigger>
        </Tip>
        <SessionMenu s={s} folded={foldedSet.has(s.id)} canFold={opts.hasKids || isFolded} onFold={(v) => setFold(s.id, v)} />
      </ContextMenu.Root>
    );
  };

  const empty = groups.length === 0 && ungrouped.length === 0;

  return (
    <div className="flex h-full flex-col">
      <div className="@container flex h-9 shrink-0 items-center gap-1 border-b border-border px-2" role="toolbar" aria-label="Map">
        <Select className="h-7 w-32 text-caption" value={kind} onChange={(e) => setKind(e.target.value as LayoutKind)} aria-label="Layout">
          {Object.entries(LAYOUTS).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </Select>
        <Tip label={roomy ? "Roomy: wide spacing, roles on the cards" : "Compact: tight spacing, roles in the tooltip"}>
          <Button size="sm" variant="ghost" onClick={() => setRoomy(!roomy)} aria-label={roomy ? "Roomy" : "Compact"}>
            <Layers /> <span className="hidden @[620px]:inline">{roomy ? "Roomy" : "Compact"}</span>
          </Button>
        </Tip>
        <Tip label={anyUnfolded ? "Fold all groups" : "Unfold all groups"}>
          <Button size="sm" variant="ghost" disabled={allFoldable.length === 0} onClick={() => setFoldedState(anyUnfolded ? allFoldable : [])} aria-label={anyUnfolded ? "Fold all" : "Unfold all"}>
            {anyUnfolded ? <FoldVertical /> : <UnfoldVertical />} <span className="hidden @[620px]:inline">{anyUnfolded ? "Fold all" : "Unfold all"}</span>
          </Button>
        </Tip>
        <div className="mx-1 h-4 w-px bg-border" />
        <Button size="icon" variant="ghost" aria-label="Zoom out" onClick={() => setZoom(Math.max(0.4, +(zoom - 0.1).toFixed(2)))}>
          <Minus />
        </Button>
        <span className="w-10 text-center font-mono text-caption text-muted-foreground">{Math.round(zoom * 100)}%</span>
        <Button size="icon" variant="ghost" aria-label="Zoom in" onClick={() => setZoom(Math.min(2, +(zoom + 0.1).toFixed(2)))}>
          <Plus />
        </Button>
        <Tip label="Reset zoom">
          <Button size="icon" variant="ghost" aria-label="Reset zoom" onClick={() => setZoom(1)}>
            <Scan />
          </Button>
        </Tip>
        <Tip label="Refresh">
          <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => app.conn.send({ type: "state.subscribe" })}>
            <RefreshCw />
          </Button>
        </Tip>
      </div>

      {pendingLaunch.length > 0 ? (
        <div className="flex items-center gap-2 border-b border-primary/30 bg-primary/10 px-3 py-1.5 text-label">
          <Rocket className="size-4 text-primary" />
          <span className="flex-1 truncate">
            Team launch ready: <strong>{pendingLaunch[0].detail}</strong>
            {pendingLaunch.length > 1 ? ` (+${pendingLaunch.length - 1} more)` : ""}
          </span>
          <Button size="sm" variant="default" onClick={() => app.showDialog(<ProposalSheet taskId={pendingLaunch[0].task_id ?? ""} />)}>
            Review
          </Button>
          <Button size="sm" variant="ghost" onClick={() => void app.call("approval.decide", { id: pendingLaunch[0].id, decision: "deny" })}>
            Decline
          </Button>
        </div>
      ) : null}

      <div className="relative flex min-h-0 flex-1">
        <ContextMenu.Root>
          <ContextMenu.Trigger asChild>
            <div
              className="min-w-0 flex-1 overflow-auto bg-background font-mono"
              onDragOver={(e) => {
                if ([DND.session, DND.repo, DND.skill].some((t) => e.dataTransfer.types.includes(t))) e.preventDefault();
              }}
              onDrop={canvasDrop}
            >
              {empty ? (
                <div className="h-full">
                  <Empty icon={<Users />} title="No agent sessions yet">
                    Start one with <strong>New session</strong> (⌘N), drag a helper from the Helper pool here, or right-click for more. Or{" "}
                    <button type="button" className="underline" onClick={app.welcome.open}>
                      show Welcome
                    </button>
                    .
                  </Empty>
                </div>
              ) : (
                <div className="flex min-h-full w-max origin-top-left items-start gap-6 p-5" style={{ transform: `scale(${zoom})`, minWidth: `${100 / Math.min(zoom, 1)}%` }}>
                  <div className="flex flex-1 flex-wrap content-start items-start gap-5">
                    {groups.map((g) => (
                      <section
                        key={g.team.id}
                        aria-label={`Team ${g.team.name}`}
                        className={cn("relative rounded-[10px]", roomy ? "border border-border bg-panel/35" : "bg-foreground/[0.04]")}
                        style={{ padding: pad, paddingTop: pad + 22 }}
                        onDragOver={frameOver}
                        onDrop={(e) => frameDrop(e, g.team)}
                      >
                        <div className="group/title absolute left-3 right-3 top-2 flex items-center gap-1.5 text-[10px] font-semibold text-muted-foreground">
                          <ListTree className="size-3" />
                          <span className="truncate" onDoubleClick={() => app.showDialog(<RenameSheet team={g.team} />)}>
                            {g.team.name}
                          </span>
                          <span className="font-normal tabular-nums text-faint">{g.team.members.filter((m) => m.state === "active").length}</span>
                          <Tip label="Rename group">
                            <button
                              type="button"
                              aria-label={`Rename group ${g.team.name}`}
                              onClick={() => app.showDialog(<RenameSheet team={g.team} />)}
                              className="rounded p-0.5 text-faint opacity-0 hover:bg-hover hover:text-foreground focus-visible:opacity-100 group-hover/title:opacity-100"
                            >
                              <Pencil className="size-3" />
                            </button>
                          </Tip>
                          <Tip label="Group settings">
                            <button
                              type="button"
                              aria-label={`Group settings ${g.team.name}`}
                              onClick={() => app.showDialog(<TeamSettingsSheet team={g.team} />)}
                              className="rounded p-0.5 text-faint opacity-0 hover:bg-hover hover:text-foreground focus-visible:opacity-100 group-hover/title:opacity-100"
                            >
                              <Settings2 className="size-3" />
                            </button>
                          </Tip>
                          <Tip label="Close group">
                            <button
                              type="button"
                              aria-label={`Close group ${g.team.name}`}
                              onClick={() => app.showDialog(<CloseTeamSheet team={g.team} />)}
                              className="rounded p-0.5 text-faint opacity-0 hover:bg-hover hover:text-foreground focus-visible:opacity-100 group-hover/title:opacity-100"
                            >
                              <X className="size-3" />
                            </button>
                          </Tip>
                        </div>
                        <div className="relative" style={{ width: g.block.w, height: g.block.h }}>
                          <svg className="pointer-events-none absolute inset-0 overflow-visible" width={g.block.w} height={g.block.h}>
                            {/* Three layers, so a line that shares a trunk with its siblings is never hidden by them:
                                every route grey under one opacity (overlaps don't darken), the lit ones on top,
                                oldest first, then the beads over everything. */}
                            <g opacity={0.75}>
                              {g.block.routes.map((r) => (
                                <path key={`${r.from}>${r.to}`} d={rounded(r.points)} fill="none" strokeLinejoin="round" stroke="var(--muted-foreground)" strokeWidth={2} />
                              ))}
                            </g>
                            {(() => {
                              const on = lit(g.block.routes).map((x) => ({ ...x, d: rounded(x.r.points) }));
                              return (
                                <>
                                  {on.map(({ r, dir, d }) => (
                                    <path key={`${r.from}>${r.to}`} d={d} fill="none" strokeLinejoin="round" stroke={DIR_COLOR[dir]} strokeWidth={2} />
                                  ))}
                                  {on.map(({ r, dir, d }) => (
                                    // Round-capped zero-length dashes are beads; the dash offset runs them the message's way.
                                    <path key={`${r.from}>${r.to}`} d={d} fill="none" stroke={DIR_COLOR[dir]} strokeWidth={5} strokeLinecap="round" strokeDasharray="0 14" className={`bead-${dir}`} />
                                  ))}
                                </>
                              );
                            })()}
                          </svg>
                          {g.block.routes.map((r) => {
                            const on = asksOn(r.from, r.to);
                            if (on.length === 0) return null;
                            const [x, y] = r.points[Math.floor(r.points.length / 2)];
                            return (
                              <AskPill
                                key={`ask:${r.from}>${r.to}`}
                                asks={on}
                                name={(id) => byId.get(id)?.name ?? id.slice(0, 8)}
                                className="absolute z-10 -translate-x-1/2 -translate-y-1/2"
                                style={{ left: x, top: y }}
                              />
                            );
                          })}
                          {Object.entries(g.block.cards).map(([id, box]) => {
                            const s = byId.get(id);
                            if (s === undefined) {
                              return (
                                <div
                                  key={id}
                                  className="absolute grid grid-cols-1 place-items-center rounded-lg border border-dashed border-input text-caption text-faint"
                                  style={{ left: box.x, top: box.y, width: box.w, height: box.h }}
                                >
                                  not running
                                </div>
                              );
                            }
                            const hasKids =
                              g.team.members.some((m) => m.state === "active" && m.parent === id) ||
                              (g.team.members.find((m) => m.session_id === id)?.role === "main" && g.team.members.filter((m) => m.state === "active").length > 1);
                            return card(s, box, { teamId: g.team.id, hidden: g.hidden[id], hasKids });
                          })}
                        </div>
                      </section>
                    ))}
                  </div>
                  {ungrouped.length > 0 ? (
                    <section
                      aria-label="Ungrouped sessions"
                      className={cn("shrink-0", groups.length > 0 && "self-stretch border-l border-border pl-6")}
                      onDragOver={frameOver}
                      onDrop={(e) => frameDrop(e, undefined)}
                    >
                      <p className="mb-2 text-[10px] tracking-[0.14em] text-muted-foreground">
                        UNGROUPED <span className="tabular-nums text-faint">{ungrouped.length}</span>
                      </p>
                      <div className="relative" style={{ width: Math.max(...ungrouped.map((s) => sizeOf(s.id).w)), height: ungrouped.reduce((y, s) => y + sizeOf(s.id).h + S.stack, -S.stack) }}>
                        {ungrouped.map((s, i) => card(s, { x: 0, y: ungrouped.slice(0, i).reduce((y, u) => y + sizeOf(u.id).h + S.stack, 0), ...sizeOf(s.id) }, { hasKids: false, lone: true }))}
                      </div>
                    </section>
                  ) : null}
                </div>
              )}
            </div>
          </ContextMenu.Trigger>
          <MenuContent>
            <Item icon={<Plus />} onSelect={() => app.showDialog(<NewSessionDialog onClose={() => app.showDialog(null)} />)}>
              New session…
            </Item>
            <Item icon={<History />} onSelect={() => app.showDialog(<PreviousSheet />)}>
              Resume previous sessions…
            </Item>
            <Sep />
            <Item icon={anyUnfolded ? <FoldVertical /> : <UnfoldVertical />} disabled={allFoldable.length === 0} onSelect={() => setFoldedState(anyUnfolded ? allFoldable : [])}>
              {anyUnfolded ? "Fold all groups" : "Unfold all groups"}
            </Item>
            <Item icon={<BookOpen />} onSelect={() => setDrawer(!drawer)}>
              {drawer ? "Hide Helper pool" : "Show Helper pool"}
            </Item>
            {domain.teams.some((t) => t.state !== "active") ? (
              <Item icon={<Archive />} onSelect={() => app.showDialog(<ClosedTeamsSheet />)}>
                Closed groups…
              </Item>
            ) : null}
          </MenuContent>
        </ContextMenu.Root>
        <AgentPool open={drawer} setOpen={setDrawer} width={drawerW} setWidth={setDrawerW} flash={flash} />
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-0.5 whitespace-nowrap border-t border-border px-3 py-1.5 text-micro text-muted-foreground">
        <Legend color="var(--faint)">parent → child</Legend>
        <Legend color="var(--route-down)" thick>
          message sent
        </Legend>
        <Legend color="var(--route-up)" thick>
          reply
        </Legend>
        <Legend color="var(--route-other)" thick>
          other message
        </Legend>
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-2.5 w-4 rounded-sm border border-dashed border-faint" /> ungrouped
        </span>
        <span className="ml-auto min-w-0 truncate text-faint">Drag a card onto another to move it with its members · onto the canvas to make it a team · right-click for actions</span>
      </div>
    </div>
  );
}

/** Open asks as a pill: "? 4m", "? 2 · 18m", "? 18m ↑" once escalated to the owner. */
/** What the pill says in words: for its title, and for the card's own label, which hides the pill's. */
function askLabel(asks: TeamMessage[], name: (id: string) => string): string {
  const oldest = asks.reduce((o, a) => (Date.parse(a.at) < Date.parse(o.at) ? a : o));
  const minutes = Math.floor((Date.now() - Date.parse(oldest.at)) / 60000);
  return (
    (asks.length > 1 ? `${asks.length} open asks, the oldest ${minutes} minutes` : `open ask to ${name(oldest.to)}, ${minutes} minutes`) +
    (asks.some((a) => a.escalated_at !== undefined) ? ", sent to you" : "")
  );
}

function AskPill({ asks, name, className, style }: { asks: TeamMessage[]; name: (id: string) => string; className?: string; style?: React.CSSProperties }): ReactElement {
  const { text, escalated } = askPill(asks, Date.now());
  const words = askLabel(asks, name);
  const label = words.charAt(0).toUpperCase() + words.slice(1);
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className={cn(
        "rounded-[2px] bg-panel px-[3px] text-[8px] leading-[11px] tabular-nums whitespace-nowrap",
        escalated ? "border-[1.5px] border-warning font-semibold text-warning" : "border border-border text-muted-foreground",
        className,
      )}
      style={style}
    >
      {text}
    </span>
  );
}

function Legend({ color, thick, children }: { color: string; thick?: boolean; children: string }): ReactElement {
  return (
    <span className="flex items-center gap-1.5">
      <svg width="18" height="8" aria-hidden>
        <path d="M0,4 H18" stroke={color} strokeWidth={thick === true ? 3 : 1.75} />
      </svg>
      {children}
    </span>
  );
}

/** The triage model's name on the badge: "Muse Spark · Meta", "Jev" or "Ollama". */
export function triageName(provider: string): string {
  return ({ meta: "Muse Spark · Meta", jev: "Jev", ollama: "Ollama" } as Record<string, string>)[provider] ?? provider;
}

/** The badge's tooltip: the triage model's pick and its reasoning or probability, or why it could not check the task. */
function triageTitle(t: Triage): string {
  const why = t.reasoning ?? (t.probability !== undefined ? `p=${t.probability.toFixed(2)}` : "");
  const what =
    t.outcome === "unavailable"
      ? `could not check this task (${t.cause ?? "no answer"}); sent as addressed`
      : t.outcome === "rerouted"
        ? `sent this to ${t.pick ?? ""} instead of ${t.named ?? ""}: ${why}`
        : `picked ${t.pick ?? ""}: ${why}${t.override !== undefined ? ` · the sender kept its own pick: ${t.override}` : ""}`;
  return `Triage (${triageName(t.provider)}) ${what} · ${t.model ?? ""} · ${t.latency_ms} ms`;
}

/** Agent messages: every routed team message, grouped by team (the team with the newest message first),
 *  coloured by its edge (as v1's Log); click a team to fold it, a message to read it whole. */
export function MessageLog(): ReactElement {
  const app = useApp();
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [folded, setFolded] = useState<Set<string>>(new Set());
  const [older, setOlder] = useState<Record<string, RecordedMessage[]>>({});
  const msgs = [...app.domain.messages].reverse().slice(0, 60);
  const openIds = new Set(openAsks(app.domain.messages).map((a) => a.id));
  const name = (id: string): string => app.sessions.find((s) => s.id === id)?.name ?? id.slice(0, 8);
  const groups = new Map<string, TeamMessage[]>();
  for (const m of msgs) groups.set(m.team_id, [...(groups.get(m.team_id) ?? []), m]);
  const edge = (m: TeamMessage): string => {
    const team = app.domain.teams.find((t) => t.id === m.team_id);
    const parent = (id: string): string | undefined => team?.members.find((x) => x.session_id === id)?.parent;
    if (parent(m.to) === m.from) return "var(--route-down)";
    if (parent(m.from) === m.to) return "var(--route-up)";
    return "var(--route-other)";
  };
  const flip =
    (set: (f: (o: Set<string>) => Set<string>) => void) =>
    (id: string): void =>
      set((o) => {
        const n = new Set(o);
        if (!n.delete(id)) n.add(id);
        return n;
      });
  const toggle = flip(setOpen);
  const fold = flip(setFolded);
  const loadOlder = async (teamId: string, shown: TeamMessage[]): Promise<void> => {
    const h = await app.call<RecordedMessage[]>("team.messages", { team_id: teamId });
    if (h !== undefined) setOlder((o) => ({ ...o, [teamId]: olderThan(h, shown) }));
  };
  return (
    <div className="h-full overflow-auto bg-panel px-3 py-2" aria-label="Agent messages">
      <p className="mb-1 text-caption font-semibold text-muted-foreground">Agent messages · newest first</p>
      {msgs.length === 0 ? <p className="text-caption text-faint">No messages between agents yet.</p> : null}
      {[...groups].map(([teamId, ms]) => (
        <section key={teamId} className="mb-1.5">
          <h3 className="text-caption font-semibold">
            <button
              type="button"
              onClick={() => fold(teamId)}
              aria-expanded={!folded.has(teamId)}
              className="flex items-center gap-1 rounded hover:text-foreground focus-visible:outline focus-visible:outline-1"
            >
              <span className="w-3 text-faint">{folded.has(teamId) ? "▸" : "▾"}</span>
              {app.domain.teams.find((t) => t.id === teamId)?.name ?? "Team " + teamId.slice(0, 8)}
              <span className="font-normal text-faint">({ms.length})</span>
            </button>
          </h3>
          {(folded.has(teamId) ? [] : ms).map((m) => {
            const full = open.has(m.id);
            return (
              <button
                key={m.id}
                type="button"
                onClick={() => toggle(m.id)}
                aria-expanded={full}
                className={cn("flex w-full gap-x-2 rounded py-0.5 text-left font-mono text-[11.5px] hover:bg-accent/40 focus-visible:outline focus-visible:outline-1", full && "flex-wrap")}
              >
                <span className="shrink-0 text-faint">{new Date(m.at).toLocaleTimeString()}</span>
                <span className={cn("w-[10ch] shrink-0 whitespace-nowrap", TYPE_CHIP[m.type ?? "tell"]?.className)}>{TYPE_CHIP[m.type ?? "tell"]?.text ?? m.type}</span>
                {(() => {
                  const from = roleOf(app.domain, app.sessions, m.from);
                  return from !== undefined ? <RoleTag role={from} /> : <Badge>agent</Badge>;
                })()}
                <span
                  className={cn("font-semibold", full ? "shrink-0" : "min-w-0 shrink truncate")}
                  style={{ color: edge(m) }}
                  title={`${roleWord(roleOf(app.domain, app.sessions, m.from))} → ${roleWord(roleOf(app.domain, app.sessions, m.to))}`}
                >
                  {name(m.from)} → {name(m.to)}
                </span>
                {m.triage !== undefined ? (
                  <span className="shrink-0" title={triageTitle(m.triage)}>
                    <Badge tone={m.triage.outcome === "agreed" ? "info" : m.triage.outcome === "overridden" || m.triage.outcome === "rerouted" ? "warning" : "neutral"}>
                      Triage · {triageName(m.triage.provider)}
                      {m.triage.outcome === "agreed" ? "" : ` · ${m.triage.outcome}`}
                    </Badge>
                  </span>
                ) : null}
                <span
                  className={cn(
                    "min-w-0 flex-1 text-muted-foreground",
                    // Expanded, the text gets its own full-width line: beside the header it is a letter wide.
                    full ? "order-last basis-full whitespace-pre-wrap [overflow-wrap:anywhere]" : "truncate",
                    m.state === "failed" && "text-danger",
                  )}
                >
                  {m.text}
                </span>
                <span
                  className={cn("shrink-0", STATE_CELL[m.state]?.className ?? "text-faint")}
                  title={m.state === "queued" ? `Queued: not yet in ${name(m.to)}'s turn` : m.state === "delivered" ? `Delivered to ${name(m.to)}'s turn` : m.state === "failed" ? `Failed: ${m.reason ?? ""}` : undefined}
                >
                  {STATE_CELL[m.state]?.text ?? m.state}
                  {openIds.has(m.id) ? ` · ? ${age(m.at, Date.now())}` : ""}
                </span>
              </button>
            );
          })}
          {folded.has(teamId) ? null : (
            <>
              {(older[teamId] ?? []).map((m, i) => (
                <p key={`o${i}`} className="flex flex-wrap gap-x-2 py-0.5 font-mono text-[11.5px] text-muted-foreground">
                  <span className="shrink-0 text-faint">{new Date(m.at).toLocaleString()}</span>
                  <span className="shrink-0 font-semibold">
                    {m.from === "" ? "PenguPool" : name(m.from)} → {name(m.to)}
                  </span>
                  <span className="basis-full whitespace-pre-wrap [overflow-wrap:anywhere]">{m.text}</span>
                </p>
              ))}
              {older[teamId] === undefined ? (
                <button type="button" onClick={() => void loadOlder(teamId, ms)} className="py-0.5 text-caption text-muted-foreground hover:text-foreground">
                  Load older
                </button>
              ) : older[teamId].length === 0 ? (
                <p className="py-0.5 text-caption text-faint">No older messages.</p>
              ) : null}
            </>
          )}
        </section>
      ))}
    </div>
  );
}

export interface Chain {
  name: string;
  steps: Array<{ name: string; detail?: string; status: string }>;
}

const GLYPH: Record<string, string> = { done: "✓", active: "●", failed: "✗" };
const GLYPH_CLS: Record<string, string> = { done: "text-success", active: "text-warning", failed: "text-danger" };

/** The chain as steps.sh echoes it: `[name] step ✓ → next ● → last ○` (v1's card line). */
export function chainText(c: Chain): string {
  return `[${c.name}] ` + c.steps.map((x) => `${x.detail ? `${x.name}|${x.detail}` : x.name} ${GLYPH[x.status] ?? "○"}`).join(" → ");
}

/** The session's workflow-tracker chain on its card, wrapped as v1 did, glyphs in their state colours. */
export function StepLine({ chain }: { chain: Chain }): ReactElement {
  return (
    <span className="block text-muted-foreground [overflow-wrap:anywhere]" title={chainText(chain)}>
      <span className="text-faint">[{chain.name}]</span>{" "}
      {chain.steps.map((x, i) => (
        <span key={i} className={x.status === "active" ? "text-foreground" : undefined}>
          {i > 0 ? <span className="text-faint"> → </span> : null}
          {x.detail ? `${x.name}|${x.detail}` : x.name} <span className={GLYPH_CLS[x.status] ?? "text-faint"}>{GLYPH[x.status] ?? "○"}</span>
        </span>
      ))}
    </span>
  );
}

/** v1's state colours: green active, amber waiting on you, red lost, grey otherwise. */
function tone(st: string): string {
  return st === "working" ? "var(--success)" : st === "idle" || st === "terminal" ? "var(--warning)" : st === "approval" || st === "lost" || st === "unreachable" ? "var(--danger)" : "var(--faint)";
}
// approval has none: its label starts with "?".
const SYMBOL: Record<string, string> = { working: "●", idle: "◷", starting: "◌", stale: "○", terminal: "!", ended: "■", exited: "■", lost: "✕", unreachable: "✕" };

/** Card padding plus border, left and right: the width a card's text gets is the card's minus this. */
const CARD_INSET = 23;
/** Room the name leaves for the LEAD chip (the name's `pr-8`). */
const NAME_PAD = 32;
const PROBE_CLS = { name: "font-mono text-[12px] font-semibold leading-[14px] [overflow-wrap:anywhere]", chain: "font-mono text-[10px] leading-[14px] [overflow-wrap:anywhere]" };
let probe: HTMLDivElement | undefined;
/** Lines `text` wraps to at `width` in a card's name or chain font (14px lines), measured off-screen as v1 did. */
function linesAt(text: string, width: number, font: keyof typeof PROBE_CLS): number {
  if (probe === undefined) {
    probe = document.createElement("div");
    Object.assign(probe.style, { position: "absolute", visibility: "hidden", left: "-10000px", top: "0" });
    document.body.appendChild(probe);
  }
  probe.className = PROBE_CLS[font];
  probe.style.width = `${width}px`;
  probe.textContent = text;
  return Math.max(1, Math.round(probe.getBoundingClientRect().height / 14));
}

/** Every session's workflow-tracker chain, refreshed every 2 s (the re-render also cools message lines). */
export function useChains(): Record<string, Chain> {
  const app = useApp();
  const [chains, setChains] = useState<Record<string, Chain>>({});
  const [, tick] = useState(0);
  useEffect(() => {
    const load = (): void =>
      void app.conn.request<Record<string, Chain>>("session.steps", {}).then(
        (c) => setChains(c ?? {}),
        () => undefined,
      );
    load();
    void document.fonts?.ready.then(() => tick((n) => n + 1)); // sizes measured before the mono font loads are wrong
    const t = window.setInterval(() => {
      tick((n) => n + 1);
      load();
    }, 2000);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return chains;
}

const roleWord = (r: Role | undefined): string => (r === undefined ? "Member" : r === "sme" ? "Builder" : r[0].toUpperCase() + r.slice(1));
