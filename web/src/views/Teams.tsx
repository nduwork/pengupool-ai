import { AlertTriangle, ChevronRight, Ellipsis, Pencil, Users } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { useState, type ReactElement, type ReactNode } from "react";
import { toast } from "sonner";

import { ago, harnessLabel, sessionName, sessionStatus, useApp, usePersisted } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Badge, Card, Empty, Input, Label, Switch, Textarea, submitOnEnter } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { Member, Team } from "../protocol/domain";
import { cn } from "../lib/utils";
import { CloseHelperSheet } from "./Approvals";
import { AddMemberSheet, FormationSheet, anchorOf } from "./Formation";
import { RenameSheet } from "./map/actions";
import { Mascot } from "./Mascot";
import { chainText, useChains, type Chain } from "./map/Map";
import { PageHeader } from "./Page";
import { checkinWaiters, waitingNote } from "./waiting";

// Teams: each team reads top to bottom in the order the owner asks about it: does anything need
// me, what is it working on, who is doing what, what did it cost. Everything
// else (rename, close, settings, messages, member actions) is one click away in a menu.
// Every change to who is on a team is a formation approval; the anchor is the hub of its messages.

export function Teams(): ReactElement {
  const app = useApp();
  const active = app.domain.teams.filter((t) => t.state === "active");
  const closed = app.domain.teams.filter((t) => t.state !== "active");
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title="Teams"
        action={
          <Button onClick={() => app.showDialog(<NewTeam />)}>
            <Users /> Group sessions
          </Button>
        }
      />
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto grid max-w-5xl grid-cols-1 gap-6 p-6">
          <RunningTeams teams={active} />
          {closed.length > 0 ? (
            <details>
              <summary className="cursor-default text-label text-muted-foreground">Closed teams ({closed.length})</summary>
              <div className="mt-2 grid grid-cols-1 gap-3">
                {closed.map((t) => (
                  <ClosedTeam key={t.id} t={t} />
                ))}
              </div>
            </details>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** A closed team is a full card, folded until opened. */
function ClosedTeam({ t }: { t: Team }): ReactElement {
  const [open, setOpen] = useState(false);
  return <TeamCard t={t} open={open} onToggle={() => setOpen(!open)} attention={false} lastAt={Date.parse(t.created_at)} />;
}

/** Statuses that ask for the owner; a team holding one sorts first and is flagged when folded. */
const ATTENTION = new Set(["approval", "lost", "unreachable"]);

function RunningTeams({ teams }: { teams: Team[] }): ReactElement {
  const app = useApp();
  const [open, setOpen] = usePersisted<string[]>("teams.open", []);
  const [q, setQ] = useState("");
  const status = (id: string): string => {
    const s = app.sessions.find((x) => x.id === id);
    return s === undefined ? "ended" : sessionStatus(s);
  };
  const needs = (t: Team): boolean =>
    t.members.some((m) => m.state === "active" && ATTENTION.has(status(m.session_id))) || app.domain.approvals.some((a) => a.state === "pending" && a.team_id === t.id);
  // Latest of the team's creation, its messages and its members' harness activity.
  const last = (t: Team): number =>
    Math.max(
      Date.parse(t.created_at),
      ...app.domain.messages.filter((m) => m.team_id === t.id).map((m) => Date.parse(m.at)),
      ...t.members.map((m) => Date.parse(app.sessions.find((x) => x.id === m.session_id)?.active_at ?? "") || 0),
    );
  const words = q.trim().toLowerCase();
  const shown = teams
    .filter((t) => words === "" || [t.name, t.goal ?? "", ...t.members.map((m) => m.name)].join(" ").toLowerCase().includes(words))
    .sort((a, b) => Number(needs(b)) - Number(needs(a)) || last(b) - last(a));
  // One running team opens by itself; with more, each row stays folded until the owner opens it.
  const isOpen = (id: string): boolean => (teams.length === 1 ? !open.includes("!" + id) : open.includes(id));
  const toggle = (id: string): void => {
    const key = teams.length === 1 ? "!" + id : id;
    setOpen(open.includes(key) ? open.filter((k) => k !== key) : [...open, key]);
  };
  return (
    <section>
      {teams.length > 4 ? (
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <Input className="h-7 w-56" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter teams or members" aria-label="Filter teams" />
          <span className="flex-1" />
          <Button size="sm" variant="ghost" onClick={() => setOpen(open.length > 0 ? [] : teams.map((t) => t.id))}>
            {open.length > 0 ? "Collapse all" : "Expand all"}
          </Button>
        </div>
      ) : null}
      {teams.length === 0 ? (
        <Empty icon={<Mascot />} title="No running teams">
          A team is the unit of work: group running sessions, and its task follows your latest instruction. Nobody joins or leaves before you approve.
        </Empty>
      ) : null}
      {teams.length > 0 && shown.length === 0 ? <p className="text-label text-muted-foreground">No team matches “{q}”.</p> : null}
      <div className="grid grid-cols-1 gap-3">
        {shown.map((t) => (
          <TeamCard key={t.id} t={t} open={isOpen(t.id)} onToggle={() => toggle(t.id)} attention={needs(t)} lastAt={last(t)} />
        ))}
      </div>
    </section>
  );
}

export const spent = (t: Team | undefined): number => Object.values(t?.cost_by ?? {}).reduce((a, b) => a + b, 0);

/** A budget estimate from past work: the median spend per member on closed teams (finished jobs),
 * times the members working now. Null until a closed team has reported spend. */
export function estimateBudget(teams: Team[], members: number): { usd: number; basis: number } | null {
  const done = teams.filter((t) => t.state !== "active" && spent(t) > 0);
  const per = done.flatMap((t) => Object.values(t.cost_by ?? {}).filter((v) => v > 0)).sort((a, b) => a - b);
  if (per.length === 0 || members === 0) return null;
  const mid = Math.floor(per.length / 2);
  const median = per.length % 2 === 1 ? per[mid] : (per[mid - 1] + per[mid]) / 2;
  return { usd: median * members, basis: done.length };
}

const usd = (n: number): string => (n < 10 ? `$${n.toFixed(2)}` : `$${Math.round(n).toLocaleString()}`);

/** A member's state in a word and a shape, so it reads without a legend; colour only when it needs the owner. */
const STATE: Record<string, { glyph: string; word: string; cls: string }> = {
  working: { glyph: "●", word: "Working", cls: "text-success" },
  starting: { glyph: "○", word: "Starting", cls: "text-muted-foreground" },
  idle: { glyph: "○", word: "Idle", cls: "text-muted-foreground" },
  stale: { glyph: "○", word: "Idle", cls: "text-muted-foreground" },
  approval: { glyph: "!", word: "Needs you", cls: "text-warning" },
  lost: { glyph: "✕", word: "Lost", cls: "text-danger" },
  unreachable: { glyph: "✕", word: "Unreachable", cls: "text-danger" },
  terminal: { glyph: "○", word: "Terminal", cls: "text-muted-foreground" },
};
const ENDED = { glyph: "–", word: "Ended", cls: "text-faint" };

/** A member's current step in plain words: the active step of its workflow chain. */
function stepWords(chain: Chain | undefined, fallback: string | undefined): string {
  const st = chain?.steps.find((x) => x.status === "active");
  if (st !== undefined) return st.detail ? `${st.name}: ${st.detail}` : st.name;
  return fallback ?? "";
}

const itemCls = "flex h-7 cursor-default select-none items-center gap-2 rounded-md px-2 text-label outline-none data-[disabled]:opacity-40 data-[highlighted]:bg-hover";

function Menu({ label, children, className }: { label: string; children: ReactNode; className?: string }): ReactElement {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button size="icon" variant="ghost" aria-label={label} className={className}>
          <Ellipsis />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="end" sideOffset={4} className="z-50 min-w-52 rounded-lg border border-border bg-raised p-1 shadow-2xl">
          {children}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function Item({ onSelect, children, danger, disabled }: { onSelect: () => void; children: ReactNode; danger?: boolean; disabled?: boolean }): ReactElement {
  return (
    <DropdownMenu.Item className={cn(itemCls, danger === true && "text-danger")} onSelect={onSelect} disabled={disabled}>
      {children}
    </DropdownMenu.Item>
  );
}

const Sep = (): ReactElement => <DropdownMenu.Separator className="my-1 h-px bg-border" />;

interface Need {
  key: string;
  tone: "warning" | "danger";
  text: ReactNode;
  /** Only the first strip's action is the view's primary button; the rest are secondary. */
  actions: (variant: "default" | "secondary") => ReactNode;
}

/** What on this team needs the owner, most pressing first. */
function useNeeds(t: Team): Need[] {
  const app = useApp();
  const out: Need[] = [];
  for (const a of app.domain.approvals.filter((x) => x.state === "pending" && x.team_id === t.id)) {
    const by = a.change?.requested_by ? sessionName(app.sessions, a.change.requested_by) : "";
    const decide = (decision: string): void => void app.call("approval.decide", { id: a.id, decision });
    out.push({
      key: a.id,
      tone: "warning",
      text: (
        <>
          {by !== "" ? <strong className="font-medium">{by} asks: </strong> : null}
          {a.detail}
        </>
      ),
      actions: (v) => (
        <>
          <Button size="sm" variant="ghost" onClick={() => decide("deny")}>
            Decline
          </Button>
          {a.change?.op === "add" ? (
            <Button size="sm" variant={v} onClick={() => app.showDialog(<FormationSheet team={t} change={a.change ?? { op: "add" }} approvalId={a.id} />)}>
              Review…
            </Button>
          ) : (
            <Button size="sm" variant={v} onClick={() => decide("approve")}>
              Approve
            </Button>
          )}
        </>
      ),
    });
  }
  const statusOf = (id: string): string => {
    const s = app.sessions.find((x) => x.id === id);
    return s === undefined ? "ended" : sessionStatus(s);
  };
  const members = t.members.filter((x) => x.state === "active");
  for (const m of members.filter((x) => checkinWaiters([x.session_id], app.gates, statusOf).length > 0))
    out.push({
      key: "checkin-" + m.session_id,
      tone: "warning",
      text: `${m.name} waits on you (check-in)`,
      actions: (v) => (
        <Button size="sm" variant={v} onClick={() => app.select(m.session_id)}>
          Open terminal
        </Button>
      ),
    });
  for (const m of members) {
    const s = app.sessions.find((x) => x.id === m.session_id);
    const st = s === undefined ? "ended" : sessionStatus(s);
    if (st === "approval")
      out.push({
        key: m.session_id,
        tone: "warning",
        text: `${m.name} needs you in its terminal`,
        actions: (v) => (
          <Button size="sm" variant={v} onClick={() => app.select(m.session_id)}>
            Open terminal
          </Button>
        ),
      });
    if (st === "lost" || st === "unreachable")
      out.push({
        key: m.session_id,
        tone: "danger",
        text: `${m.name} lost its terminal${s?.active_at ? ` ${ago(s.active_at)}` : ""}.`,
        actions: (v) => (
          <Button size="sm" variant={v} onClick={() => void app.call("session.restart", { session_id: m.session_id })}>
            Restart {m.name}
          </Button>
        ),
      });
  }
  if (t.budget_usd !== undefined && t.budget_usd > 0 && spent(t) >= t.budget_usd)
    out.push({
      key: "budget",
      tone: "warning",
      text: `Spent ${usd(spent(t))} of its ${usd(t.budget_usd)} budget.`,
      actions: (v) => (
        <Button size="sm" variant={v} onClick={() => app.showDialog(<TeamSettingsSheet team={t} />)}>
          Change budget
        </Button>
      ),
    });
  return out;
}

function TeamCard({ t, open, onToggle, attention, lastAt }: { t: Team; open: boolean; onToggle: () => void; attention: boolean; lastAt: number }): ReactElement {
  const app = useApp();
  const needs = useNeeds(t);
  const isActive = t.state === "active";
  const bodyId = `team-${t.id}`;
  const show = (d: ReactElement): void => app.showDialog(d);
  return (
    <Card className={cn("overflow-hidden", attention && !open && "border-warning/60")}>
      <div className="flex min-w-0 items-center gap-2 py-1.5 pr-2 pl-1.5">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          aria-controls={bodyId}
          className="flex min-w-0 flex-1 items-center gap-2 rounded-md p-1.5 text-left hover:bg-muted/60 focus-visible:outline-2 focus-visible:outline-primary"
        >
          <ChevronRight className={cn("size-4 shrink-0 text-faint transition-transform motion-reduce:transition-none", open && "rotate-90")} />
          <span className="shrink-0 text-body font-semibold">{t.name}</span>
          <span className="min-w-0 flex-1 truncate text-label text-muted-foreground">{open ? "" : (t.goal ?? "")}</span>
          <span className="hidden shrink-0 min-w-14 text-right text-caption text-faint sm:inline">{isActive ? ago(new Date(lastAt).toISOString()) : `started ${ago(t.created_at)}`}</span>
        </button>
        {open ? (
          <>
            <Menu label={`More for ${t.name}`}>
              <Item onSelect={() => show(<MembersSheet team={t} />)}>Members…</Item>
              {isActive ? <Item onSelect={() => show(<AddMemberSheet team={t} />)}>Add member…</Item> : null}
              {isActive ? <Item onSelect={() => show(<TaskSheet team={t} />)}>Task…</Item> : null}
              <Item onSelect={() => show(<TeamSettingsSheet team={t} />)}>Settings…</Item>
              <Item onSelect={() => show(<RenameSheet team={t} />)}>Rename…</Item>
              <Sep />
              {isActive ? (
                <Item danger onSelect={() => show(<CloseTeamSheet team={t} />)}>
                  Close team…
                </Item>
              ) : (
                <Item danger onSelect={() => show(<DeleteTeamSheet team={t} />)}>
                  Delete team…
                </Item>
              )}
            </Menu>
          </>
        ) : null}
      </div>
      {open ? (
        <div id={bodyId} className="border-t border-border">
          {needs.length > 0 ? (
            <div className="grid gap-1.5 px-3 pt-3">
              {needs.map((n, i) => (
                <div
                  key={n.key}
                  className={cn(
                    "flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md border px-3 py-2 text-label",
                    n.tone === "danger" ? "border-danger/40 bg-danger/10" : "border-warning/40 bg-warning/10",
                  )}
                  role="status"
                >
                  <AlertTriangle className={cn("size-4 shrink-0", n.tone === "danger" ? "text-danger" : "text-warning")} />
                  <span className="min-w-0 flex-1">{n.text}</span>
                  <span className="flex shrink-0 gap-1.5">{n.actions(i === 0 ? "default" : "secondary")}</span>
                </div>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}

/** What the team works on: the owner's words. */
function WorkingOn({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const [editing, setEditing] = useState(false);
  const [goal, setGoal] = useState(team.goal ?? "");
  if (editing)
    return (
      <form
        className="grid grid-cols-1 gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          if ((await app.call("team.goal", { team_id: team.id, goal })) !== undefined) setEditing(false);
        }}
      >
        <Textarea autoFocus className="min-h-20" value={goal} onChange={(e) => setGoal(e.target.value)} onKeyDown={submitOnEnter} aria-label="Team task" />
        <p className="text-caption text-muted-foreground">Every member is told, and the change goes on the team's record as a direction change.</p>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
            Cancel
          </Button>
          <Button type="submit" size="sm" variant="default" disabled={goal.trim() === ""}>
            Save task
          </Button>
        </div>
      </form>
    );
  return (
    <div>
      <p className="text-caption text-faint">Working on</p>
      <div className="mt-0.5 flex items-start gap-2">
        <p className={cn("line-clamp-3 min-w-0 flex-1 text-label [overflow-wrap:anywhere]", team.goal ? "text-foreground" : "text-faint")} title={team.goal}>
          {team.goal || "No task yet: your next instruction to a member becomes the team's task."}
        </p>
        <Button
          size="icon"
          variant="ghost"
          aria-label={team.goal ? "Edit the task" : "Set the task"}
          title={team.goal ? "Edit the task" : "Set the task"}
          onClick={() => {
            setGoal(team.goal ?? "");
            setEditing(true);
          }}
        >
          <Pencil />
        </Button>
      </div>
    </div>
  );
}

/** One row per member: state, name, role, current step, last activity; the rest in its menu. */
function Members({ team, all }: { team: Team; all: boolean }): ReactElement {
  const app = useApp();
  const chains = useChains();
  const anchor = anchorOf(app.domain, team);
  const members = team.members.filter((m) => all || m.state === "active");
  return (
    <ul className="mt-4 grid gap-0.5" aria-label="Members">
      {members.map((m) => {
        const s = app.sessions.find((x) => x.id === m.session_id);
        const st = s === undefined ? ENDED : (STATE[sessionStatus(s)] ?? ENDED);
        const chain = chains[m.session_id];
        const role = [m.role === "main" ? "main" : m.role === "sme" ? "SME" : "", m.session_id === anchor ? "anchor" : ""].filter((x) => x !== "").join(" · ");
        return (
          <li key={m.session_id} className="group flex h-8 min-w-0 items-center gap-3 rounded-md px-1 text-label hover:bg-muted/50">
            <span className={cn("w-24 shrink-0 text-caption", st.cls)}>
              <span aria-hidden className="inline-block w-3">
                {st.glyph}
              </span>
              {st.word}
            </span>
            <button type="button" className="max-w-56 min-w-0 truncate font-medium hover:underline focus-visible:outline-2 focus-visible:outline-primary" onClick={() => app.select(m.session_id)}>
              {m.name}
            </button>
            {role !== "" ? <span className="shrink-0 text-caption text-faint">{role}</span> : null}
            <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground" title={chain !== undefined ? chainText(chain) : undefined}>
              {waitingNote(s?.waiting) ?? stepWords(chain, s?.summary)}
            </span>
            <span className="w-14 shrink-0 text-right text-caption text-faint">{ago(s?.active_at)}</span>
            <MemberMenu team={team} m={m} isAnchor={m.session_id === anchor} />
          </li>
        );
      })}
    </ul>
  );
}

function MemberMenu({ team, m, isAnchor }: { team: Team; m: Member; isAnchor: boolean }): ReactElement {
  const app = useApp();
  const s = app.sessions.find((x) => x.id === m.session_id);
  const cost = team.cost_by?.[m.session_id] ?? 0;
  return (
    <Menu label={`More for ${m.name}`} className="size-6 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100">
      <DropdownMenu.Label className="px-2 py-1 text-caption text-faint">
        <span className="block text-foreground">{m.name}</span>
        {s !== undefined ? harnessLabel(s.harness) : ""}
        {cost > 0 ? ` · ${usd(cost)} spent` : ""}
        {m.branch ? <span className="block truncate font-mono text-micro">{m.branch}</span> : null}
      </DropdownMenu.Label>
      <Sep />
      <Item onSelect={() => app.select(m.session_id)}>Open terminal</Item>
      {m.branch ? (
        <Item
          onSelect={() =>
            void navigator.clipboard.writeText(m.branch ?? "").then(
              () => toast.success("Branch name copied"),
              () => toast.error("Could not copy"),
            )
          }
        >
          Copy branch name
        </Item>
      ) : null}
      {!isAnchor ? <Item onSelect={() => app.showDialog(<FormationSheet team={team} change={{ op: "anchor", session_id: m.session_id }} />)}>Make anchor</Item> : null}
      {m.role !== "main" ? (
        <>
          <Sep />
          <Item danger onSelect={() => app.showDialog(<CloseHelperSheet team={team} member={m} />)}>
            Remove from team…
          </Item>
        </>
      ) : null}
    </Menu>
  );
}

/** Task and members live off the board's face; each is one menu item, over the live team. */
function TeamPartSheet({ team, title, wide, children }: { team: Team; title: string; wide?: boolean; children: (t: Team) => ReactNode }): ReactElement {
  const app = useApp();
  const t = app.domain.teams.find((x) => x.id === team.id) ?? team;
  return (
    <Sheet title={`${title} of ${t.name}`} onClose={() => app.showDialog(null)} wide={wide}>
      {children(t)}
    </Sheet>
  );
}
const TaskSheet = ({ team }: { team: Team }): ReactElement => <TeamPartSheet team={team} title="Task">{(t) => <WorkingOn team={t} />}</TeamPartSheet>;
const MembersSheet = ({ team }: { team: Team }): ReactElement => (
  <TeamPartSheet team={team} title="Members" wide>
    {(t) => <Members team={t} all={t.state !== "active"} />}
  </TeamPartSheet>
);

/** The team's rarely changed settings: budget, phone access, group password. */
function TeamSettingsSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const t = app.domain.teams.find((x) => x.id === team.id) ?? team; // live, so switches reflect changes
  const [budget, setBudget] = useState(String(t.budget_usd ?? ""));
  const [pw, setPw] = useState("");
  return (
    <Sheet title={`Settings of ${t.name}`} onClose={() => app.showDialog(null)}>
      <div className="grid gap-5 text-label">
        <form
          className="grid gap-1.5"
          onSubmit={async (e) => {
            e.preventDefault();
            if (
              (await app.call("team.budget", {
                team_id: t.id,
                budget_usd: Number(budget) || 0,
              })) !== undefined
            )
              toast.success("Budget saved");
          }}
        >
          <Label hint="In USD; 0 clears it, and the estimate from closed teams is shown instead.">
            Budget
            <div className="flex gap-2">
              <Input type="number" min={0} step="1" className="w-32" value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="USD" />
              <Button type="submit" size="sm">
                Save
              </Button>
            </div>
          </Label>
        </form>
        <div className="flex items-center justify-between gap-3">
          <span>
            Allow iOS access
            <span className="block text-caption text-muted-foreground">The PenguPool phone app can see and message this team.</span>
          </span>
          <Switch checked={t.ios_access} onCheckedChange={(v) => void app.call("team.policy", { team_id: t.id, ios_access: v })} label="Allow iOS access" />
        </div>
        <form
          className="grid gap-1.5"
          onSubmit={async (e) => {
            e.preventDefault();
            if (
              (await app.call("team.policy", {
                team_id: t.id,
                password: pw,
              })) !== undefined
            ) {
              setPw("");
              toast.success("Group password saved");
            }
          }}
        >
          <Label hint="Asked on the phone before it opens this team. At least 4 characters.">
            Group password
            <div className="flex gap-2">
              <Input
                type="password"
                className="w-56"
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                placeholder={t.password_set ? "New group password" : "Group password"}
                autoComplete="new-password"
              />
              <Button type="submit" size="sm" disabled={pw.length < 4}>
                {t.password_set ? "Change" : "Set"}
              </Button>
              {t.password_set ? (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    void app.call("team.policy", {
                      team_id: t.id,
                      clear_password: true,
                    })
                  }
                >
                  Remove
                </Button>
              ) : null}
            </div>
          </Label>
        </form>
      </div>
    </Sheet>
  );
}

function CloseTeamSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title={`Close ${team.name}?`}
      description="Every helper is closed; the main session keeps running. The team's record stays."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Keep the team
          </Button>
          <Button
            variant="danger"
            onClick={async () => {
              if ((await app.call("team.close", { team_id: team.id })) !== undefined) close();
            }}
          >
            Close team
          </Button>
        </>
      }
    >
      <span />
    </Sheet>
  );
}

export function DeleteTeamSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const [busy, setBusy] = useState(false);
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title={`Delete ${team.name}?`}
      description="Deletes this team's helpers' team notes and raw turns. Helper agents keep their other experience; repositories and branches are untouched."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Keep the team
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              if ((await app.call("team.delete", { team_id: team.id })) !== undefined) close();
              else setBusy(false);
            }}
          >
            {busy ? "Deleting…" : "Delete team"}
          </Button>
        </>
      }
    >
      <span />
    </Sheet>
  );
}

function NewTeam(): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const free = app.sessions.filter((s) => s.state === "running" && s.team_id === undefined && (s.harness === "claude" || s.harness === "omp"));
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  return (
    <Sheet
      title="Group running sessions"
      description="The first session you pick is the main worker. One harness per team, at most four."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant="default"
            disabled={picked.length === 0 || name === ""}
            onClick={async () => {
              if (
                (await app.call("team.create", {
                  name,
                  goal,
                  session_ids: picked,
                })) !== undefined
              )
                close();
            }}
          >
            Create team
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-1 gap-4">
        <Label>
          Team name
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Label>
        <Label>
          Goal
          <Input value={goal} onChange={(e) => setGoal(e.target.value)} />
        </Label>
        <div className="grid grid-cols-1 gap-1">
          {free.length === 0 ? <p className="text-label text-muted-foreground">No ungrouped agent sessions are running.</p> : null}
          {free.map((s) => (
            <label key={s.id} className="flex items-center gap-2 rounded-md p-2 text-label hover:bg-hover">
              <input type="checkbox" checked={picked.includes(s.id)} onChange={(e) => setPicked(e.target.checked ? [...picked, s.id] : picked.filter((x) => x !== s.id))} />
              <span className="font-medium">{s.name}</span>
              <span className="truncate text-caption text-faint">
                {harnessLabel(s.harness)} · {s.cwd}
              </span>
              {picked[0] === s.id ? <Badge tone="primary">Main</Badge> : null}
            </label>
          ))}
        </div>
      </div>
    </Sheet>
  );
}
