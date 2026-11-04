import { Bell, MessageCircleQuestion, MessagesSquare, ShieldQuestion, Rocket, ThumbsDown, ThumbsUp, Users, X } from "lucide-react";
import { Popover } from "radix-ui";
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactElement, type ReactNode } from "react";

import { AGENTS, type AppCtx, ago, harnessLabel, sessionName, skillName, type SpotCheck, teamOf, useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Badge, Input, Label, RoleTag, Select, Textarea, submitOnEnter } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import { cn } from "../lib/utils";
import type { Approval, Member, PerCompass, Proposal, ProposedMember, Team } from "../protocol/domain";
import { ownerViaLabel, perCompassLabel, type Line } from "./compass";
import { FormationSheet, roleOf } from "./Formation";
import { type Need, approvalItem, dismissKey, needItem, spotItem, waitingOnOwner } from "./inbox";
import { age } from "./map/asks";

// One surface for what waits on the owner: the bell in the title bar, its count the whole list
// (waitingOnOwner, also the dock badge), and its panel the list itself: team launches and changes,
// an agent's question, a check-in (Go on, Stop, or a reply typed into its terminal), a permission
// dialog (it lives in the harness, so Open terminal), a go-ahead triage picked for the owner to
// check, and a member's ask nobody answered. An item leaves as soon as it is answered, anywhere.
// Every item is one row: a meta line (who, team, age), the question wrapped (never cut to one line),
// a context block, and its actions on their own wrapping line, so nothing runs out of the panel.

/** The bell's panel: 440px or the window less a margin, as tall as the window allows; the list scrolls under a fixed header. */
export const PANEL = "z-50 flex outline-none max-h-[min(36rem,var(--radix-popover-content-available-height))] w-[440px] max-w-[calc(100vw-16px)] flex-col rounded-lg border border-border bg-raised shadow-2xl";

export function ApprovalsBell(): ReactElement {
  const app = useApp();
  const { items } = waitingOnOwner(app.domain.approvals, app.sessions, app.domain.messages, app.gates, app.spots, app.dismissed);
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <Button size="icon" variant="ghost" aria-label={`${items.length} waiting on you`} className="relative">
          <Bell />
          {items.length > 0 ? (
            <span className="absolute -right-0.5 -top-0.5 grid grid-cols-1 min-w-4 place-items-center rounded-full bg-warning px-1 text-micro font-bold text-black">{items.length}</span>
          ) : null}
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={6}
          collisionPadding={8}
          tabIndex={-1}
          className={PANEL}
          // Focus the panel, not its first button: Enter right after opening must not answer an agent.
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            (e.currentTarget as HTMLElement).focus();
          }}
        >
          <p className="flex shrink-0 items-center justify-between border-b border-border px-4 py-2 text-label font-semibold">
            Waiting on you <span className="tabular-nums text-muted-foreground">{items.length}</span>
          </p>
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2">
            <WaitingList />
            {open ? <DecidedForYou /> : null}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** The panel's list, in waitingOnOwner's order: approvals, then what sessions need, then members' asks, then go-aheads to check. */
export function WaitingList(): ReactElement {
  const app = useApp();
  const { approvals, needs, spots } = waitingOnOwner(app.domain.approvals, app.sessions, app.domain.messages, app.gates, app.spots, app.dismissed);
  const { verdict } = useSpotChecks();
  const list = useRef<HTMLDivElement>(null);
  const focusedRow = useRef<number | null>(null); // the row holding focus (or last clicked), kept when its row leaves: in-panel answer or answered elsewhere
  const keys = [...approvals.map((a) => a.id), ...needs.map((n) => n.key ?? n.session), ...spots.map((g) => g.id)].join(" ");
  useLayoutEffect(() => {
    const from = focusedRow.current;
    if (from === null || (document.activeElement !== null && document.activeElement !== document.body)) return;
    focusedRow.current = null;
    const rows = list.current?.querySelectorAll<HTMLElement>(":scope > [role=listitem]") ?? [];
    const i = refocusRow(from, rows.length);
    // The row itself, not its first button: Enter must not answer the next agent.
    (rows[i] ?? list.current?.closest<HTMLElement>("[role=dialog]"))?.focus();
  }, [keys]);
  // The empty state keeps the ref, so when the last row leaves, focus can still find the panel.
  if (approvals.length + needs.length + spots.length === 0)
    return (
      <div ref={list}>
        <p className="px-2 py-3 text-label text-muted-foreground">Nothing is waiting for you.</p>
      </div>
    );
  return (
    <div
      role="list"
      ref={list}
      className="grid grid-cols-1 gap-1"
      // A click records the row too: a button that answers its row may leave before it holds focus.
      onFocus={(e) => (focusedRow.current = [...(list.current?.children ?? [])].findIndex((r) => r.contains(e.target)))}
      onPointerDown={(e) => (focusedRow.current = [...(list.current?.children ?? [])].findIndex((r) => r.contains(e.target as Node)))}
      onBlur={(e) => {
        if (e.relatedTarget !== null) focusedRow.current = null;
      }}
    >
      {approvals.map((a) => (
        <Dismiss.Provider key={a.id} value={dismissKey(approvalItem(a))}>
          {a.kind === "ask" ? <AgentAskRow a={a} /> : a.kind === "formation" ? <FormationRow a={a} /> : <LaunchRow a={a} />}
        </Dismiss.Provider>
      ))}
      {needs.map((n) => (
        <Dismiss.Provider key={n.key ?? n.session} value={dismissKey(needItem(n))}>
          {n.kind === "ask" ? <AskRow n={n} /> : <NeedRow n={n} />}
        </Dismiss.Provider>
      ))}
      {spots.map((g) => (
        <Dismiss.Provider key={g.id} value={dismissKey(spotItem(g))}>
          <WaitRow icon={<ThumbsUp className="size-4 shrink-0 text-warning" />} label={`${g.agent}: go-ahead to check`} who={<strong className="font-semibold">{g.agent}</strong>} meta={teamName(app, g.team_id)} at={g.created_at}>
            <Clamp lines={4}>Was this go-ahead right? {g.question}</Clamp>
            <Actions>
              <Button size="sm" variant="default" onClick={() => verdict(g.id, true)}>
                <ThumbsUp /> Right
              </Button>
              <Button size="sm" onClick={() => verdict(g.id, false)}>
                <ThumbsDown /> Wrong
              </Button>
            </Actions>
          </WaitRow>
        </Dismiss.Provider>
      ))}
    </div>
  );
}

/** The row to focus (the row, not a button in it) when the focused row left the list: the one now at its index, else the last; -1 is none (focus the panel). */
export const refocusRow = (index: number, rows: number): number => (rows === 0 ? -1 : Math.min(index, rows - 1));

const teamName = (app: AppCtx, teamId: string | undefined, session?: string): string | undefined =>
  app.domain.teams.find((t) => t.id === teamId)?.name ?? (session !== undefined ? teamOf(app.domain, session)?.name : undefined);

/** The row's dismissKey (inbox), set by WaitingList around each row: WaitRow's × hides that item until it changes. */
const Dismiss = createContext<string | undefined>(undefined);

/** One item in the panel: icon, who (never cut), team or other meta (cut first), age and expiry at the right, and ×; then the body. */
function WaitRow({ icon, label, who, meta, at, after, children }: { icon: ReactElement; label: string; who?: ReactNode; meta?: string; at?: string; after?: string; children: ReactNode }): ReactElement {
  const app = useApp();
  const dismiss = useContext(Dismiss);
  return (
    <div role="listitem" aria-label={label} tabIndex={-1} className="grid min-w-0 grid-cols-1 gap-1.5 rounded-md p-2 text-label outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-ring">
      <div className="flex min-w-0 items-start gap-1.5">
        <span className="mt-0.5 shrink-0">{icon}</span>
        <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5">
          {who !== undefined ? <span className="flex min-w-0 flex-wrap items-center gap-1.5 [overflow-wrap:anywhere]">{who}</span> : null}
          {meta !== undefined && meta !== "" ? <span className="min-w-0 truncate text-caption text-muted-foreground">{who !== undefined ? "· " : ""}{meta}</span> : null}
        </span>
        <span className="ml-auto shrink-0 pl-2 text-caption tabular-nums text-muted-foreground">
          {at !== undefined && at !== "" ? age(at, Date.now()) : ""}
          {after !== undefined ? ` · ${after}` : ""}
        </span>
        {dismiss !== undefined ? (
          <button
            type="button"
            onClick={() => app.dismiss(dismiss)}
            aria-label={`Dismiss ${label}`}
            title="Dismiss: hidden until it changes or the app restarts; it still waits on you"
            className="-my-0.5 shrink-0 rounded p-0.5 text-faint outline-none hover:bg-hover hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="size-3.5" />
          </button>
        ) : null}
      </div>
      {children}
    </div>
  );
}

/** A row's actions: their own line, right-aligned, wrapping rather than shrinking. */
function Actions({ children }: { children: ReactNode }): ReactElement {
  return <div className="flex flex-wrap justify-end gap-1.5">{children}</div>;
}

const CLAMP = { 2: "line-clamp-2", 3: "line-clamp-3", 4: "line-clamp-4" } as const;

/**
 * Text that wraps inside the row, cut to a few lines with "Show all" when it is longer (measured, so
 * the link shows only when something is hidden). A context block (mono) opens to max-h-48 with its own
 * scroll, so the row stays short either way.
 */
function Clamp({ lines, mono, className, children }: { lines: 2 | 3 | 4; mono?: boolean; className?: string; children: ReactNode }): ReactElement {
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [over, setOver] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el !== null && !open) setOver(el.scrollHeight > el.clientHeight + 1);
  }, [open, children]);
  const text = (
    <div ref={ref} className={cn("min-w-0 whitespace-pre-wrap [overflow-wrap:anywhere]", open ? mono && "max-h-48 overflow-auto" : CLAMP[lines], !mono && className)}>
      {children}
    </div>
  );
  return (
    <div className="grid min-w-0 grid-cols-1 justify-items-start gap-0.5">
      {mono ? <div className="w-full min-w-0 rounded-md bg-hover p-2 font-mono text-caption">{text}</div> : text}
      {over || open ? (
        <button type="button" className="text-caption text-primary hover:underline" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show all"}
        </button>
      ) : null}
    </div>
  );
}

/** What was decided for the owner this week: a lead's per-Compass decisions and refusals (answer.per_compass),
 *  and asks answered with the owner's words typed in another session. Loaded each time the bell opens. */
function DecidedForYou(): ReactElement | null {
  const app = useApp();
  const [records, setRecords] = useState<PerCompass[]>([]);
  const [lines, setLines] = useState<Line[]>([]);
  const [rev, setRev] = useState(0);
  useEffect(() => {
    void app.call<PerCompass[]>("answer.per_compass", {}).then((r) => setRecords(r ?? []));
    void app.call<{ lines: Line[] }>("compass.list", { query: "" }).then((r) => setLines(r?.lines ?? []));
  }, [rev]);
  const name = (id: string): string => sessionName(app.sessions, id);
  const via = app.domain.approvals.filter((a) => a.kind === "ask" && a.state !== "pending" && ownerViaLabel(a.answered_by, name) !== undefined);
  if (records.length === 0 && via.length === 0) return null;
  return (
    <>
      <p className="flex items-center justify-between px-2 pb-1 pt-2 text-label font-semibold">
        Decided for you <span className="tabular-nums text-muted-foreground">{records.length + via.length}</span>
      </p>
      <div className="grid grid-cols-1 gap-1">
        {records.map((r) => (
          <PerCompassRow key={r.item + r.at} r={r} label={perCompassLabel(r, name, lines)} wrong={app.domain.approvals.find((a) => a.id === r.item)?.wrong === true} onChange={() => setRev(rev + 1)} />
        ))}
        {via.map((a) => (
          <div key={a.id} className="grid gap-0.5 rounded-md p-2 text-label hover:bg-hover">
            <Clamp lines={2}>
              <strong className="font-semibold">{name(a.session_id ?? "")}</strong>: {a.detail}
            </Clamp>
            <Clamp lines={2} className="text-caption text-muted-foreground">
              {ownerViaLabel(a.answered_by, name)}
              {a.owner_words !== undefined && a.owner_words !== "" ? `: “${a.owner_words}”` : ""}
            </Clamp>
          </div>
        ))}
      </div>
    </>
  );
}

/** One per-Compass record; an answered one the owner has not called wrong offers "That was wrong" with optional words. */
function PerCompassRow({ r, label, wrong, onChange }: { r: PerCompass; label: string; wrong: boolean; onChange: () => void }): ReactElement {
  const app = useApp();
  const [writing, setWriting] = useState(false);
  const [words, setWords] = useState("");
  const [done, setDone] = useState(wrong); // check-in records carry no wrong flag: hide after the owner's call
  const send = async (): Promise<void> => {
    if ((await app.call("answer.wrong", { id: r.item, words: words.trim() === "" ? undefined : words.trim() })) === undefined) return;
    setDone(true);
    setWriting(false);
    onChange();
  };
  return (
    <div className="grid gap-1 rounded-md p-2 text-label hover:bg-hover">
      <Clamp lines={2}>
        <strong className="font-semibold">{sessionName(app.sessions, r.session_id)}</strong>: {r.question}
      </Clamp>
      <Clamp lines={2} className="text-caption text-muted-foreground">
        {label}
        {r.outcome === "answered" ? ` → “${r.detail}”` : ""} · {ago(r.at)}
      </Clamp>
      {r.outcome === "answered" && !done && !wrong ? (
        writing ? (
          <form
            className="flex gap-1.5"
            onSubmit={(e) => {
              e.preventDefault();
              void send();
            }}
          >
            <Input value={words} onChange={(e) => setWords(e.target.value)} placeholder="What was right (optional)" aria-label="Why it was wrong" />
            <Button size="sm" type="submit">
              Send
            </Button>
          </form>
        ) : (
          <div className="flex justify-end">
            <Button size="sm" variant="ghost" onClick={() => setWriting(true)}>
              <ThumbsDown /> That was wrong
            </Button>
          </div>
        )
      ) : null}
    </div>
  );
}

/** The go-aheads waiting for the owner's 👍/👎, refreshed every 30 s; shared by the panel and the Compass panel. */
export function useSpotChecks(): { spots: SpotCheck[]; verdict: (id: number, right: boolean) => Promise<void> } {
  const app = useApp();
  // The list is App's (read every 30 s and after each action); a verdict is an action, so it clears at once.
  const verdict = async (id: number, right: boolean): Promise<void> => void (await app.call("compass.spot_check", { gate_id: id, right }));
  return { spots: app.spots, verdict };
}

/** A session's role tag, when it is on a team. */
function NeedRole({ id }: { id: string }): ReactElement | null {
  const app = useApp();
  const role = roleOf(app.domain, app.sessions, id);
  return role !== undefined ? <RoleTag role={role} /> : null;
}

/** A member's ask nobody answered and triage could not settle: go on without the answer, answer it
 *  in the recipient's terminal, or close it. */
function AskRow({ n }: { n: Need }): ReactElement | null {
  const app = useApp();
  const a = n.ask;
  if (a === undefined) return null;
  const settle = (action: "go_on" | "dismiss"): void => void app.call("ask.settle", { id: a.id, action });
  return (
    <WaitRow
      icon={<MessagesSquare className="size-4 shrink-0 text-warning" />}
      label={`${n.name}: ask of ${n.toName ?? ""}`}
      who={
        <>
          <strong className="font-semibold">{n.name}</strong>
          <NeedRole id={a.from} />→<strong className="font-semibold">{n.toName}</strong>
          <NeedRole id={a.to} />
        </>
      }
      meta={teamName(app, a.team_id, a.from)}
      at={a.at}
    >
      <Clamp lines={4}>“{a.text}”</Clamp>
      <Actions>
        <Button size="sm" variant="default" onClick={() => settle("go_on")}>
          Go on
        </Button>
        <Button size="sm" title={`Opens ${n.toName ?? ""}'s terminal`} onClick={() => app.select(a.to)}>
          Reply
        </Button>
        <Button size="sm" variant="ghost" onClick={() => settle("dismiss")}>
          Dismiss
        </Button>
      </Actions>
    </WaitRow>
  );
}

/** A check-in is answered here; a permission dialog or a first-run dialog lives in the harness's terminal. */
function NeedRow({ n }: { n: Need }): ReactElement {
  const app = useApp();
  const [reply, setReply] = useState(false);
  const [text, setText] = useState("");
  const [confirming, setConfirming] = useState(false);
  const answer = async (t: string): Promise<void> => {
    if ((await app.call("checkin.answer", { gate_id: n.gate?.id, text: t })) !== undefined) setReply(false);
  };
  const checkin = n.kind === "checkin";
  return (
    <WaitRow
      icon={checkin ? <MessageCircleQuestion className="size-4 shrink-0 text-warning" /> : <ShieldQuestion className="size-4 shrink-0 text-warning" />}
      label={`${n.name}: ${checkin ? "check-in" : n.kind === "permission" ? "permission" : "blocked"}`}
      who={
        <>
          <strong className="font-semibold">{n.name}</strong>
          <NeedRole id={n.session} />
          {n.gate?.floor === true ? <Badge tone="danger">floor</Badge> : null}
        </>
      }
      meta={teamName(app, n.gate?.team_id, n.session)}
      at={n.gate?.created_at}
    >
      {checkin ? (
        <Clamp lines={3} mono>
          {n.text}
        </Clamp>
      ) : (
        <Clamp lines={4}>{n.text}</Clamp>
      )}
      {n.detail !== undefined ? (
        <Clamp lines={3} mono>
          {n.detail}
        </Clamp>
      ) : null}
      {checkin && n.gate?.suggestion !== undefined && n.gate.suggestion !== "" ? (
        <div className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 text-caption text-muted-foreground">
          <span className="min-w-0 flex-1 basis-48 whitespace-pre-wrap [overflow-wrap:anywhere]">
            Suggested: “{n.gate.suggestion}”{n.gate.evidence !== undefined && n.gate.evidence !== "" ? ` — ${n.gate.evidence}` : ""}
          </span>
          <Button
            size="sm"
            disabled={confirming}
            onClick={() => {
              setConfirming(true);
              void answer(n.gate?.suggestion ?? "").finally(() => setConfirming(false));
            }}
          >
            Confirm
          </Button>
        </div>
      ) : null}
      <Actions>
        {checkin ? (
          <>
            <Button size="sm" variant="default" onClick={() => void answer("Go on.")}>
              Go on
            </Button>
            <Button size="sm" onClick={() => void answer("Stop. Do not continue; wait for my next instruction.")}>
              Stop
            </Button>
            <Button size="sm" variant="ghost" aria-expanded={reply} onClick={() => setReply(!reply)}>
              Reply…
            </Button>
          </>
        ) : null}
        <Button size="sm" variant={checkin ? "ghost" : "default"} title={checkin ? undefined : "The dialog is in the agent's terminal"} onClick={() => app.select(n.session)}>
          Open terminal
        </Button>
      </Actions>
      {reply ? (
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (text.trim() !== "") void answer(text).then(() => setText(""));
          }}
        >
          <Textarea rows={2} value={text} maxLength={2000} onChange={(e) => setText(e.target.value)} onKeyDown={submitOnEnter} placeholder="Your answer (Enter sends, Shift+Enter adds a line)" aria-label={`Reply to ${n.name}`} />
          <Button size="sm" type="submit" variant="default" disabled={text.trim() === ""}>
            Send
          </Button>
        </form>
      ) : null}
    </WaitRow>
  );
}

/** A member asked to change who is on its team: nothing changes until the owner approves. */
function FormationRow({ a }: { a: Approval }): ReactElement {
  const app = useApp();
  const decide = (decision: string): void => void app.call("approval.decide", { id: a.id, decision });
  const by = a.change?.requested_by !== undefined && a.change.requested_by !== "" ? sessionName(app.sessions, a.change.requested_by) : "";
  // Adding a member starts an agent: the owner picks which one before approving.
  const team = app.domain.teams.find((t) => t.id === a.team_id);
  return (
    <WaitRow icon={<Users className="size-4 shrink-0 text-primary" />} label={`${by !== "" ? by : "Team"}: team change`} who={<strong className="font-semibold">{by !== "" ? by : "Team change"}</strong>} meta={team?.name} at={a.created_at}>
      <Clamp lines={4}>{a.detail}</Clamp>
      <Actions>
        <Button
          size="sm"
          variant="default"
          onClick={() => (team !== undefined && a.change?.op === "add" ? app.showDialog(<FormationSheet team={team} change={a.change} approvalId={a.id} />) : decide("approve"))}
        >
          {team !== undefined && a.change?.op === "add" ? "Review…" : "Approve"}
        </Button>
        <Button size="sm" onClick={() => decide("deny")}>
          Decline
        </Button>
      </Actions>
    </WaitRow>
  );
}

/** Long choices wrap inside their button (Button is nowrap by design); stacked one per line when any is long. */
const OPTION = "h-auto min-h-7 whitespace-normal py-1 text-left [overflow-wrap:anywhere]";

/** An agent put a question to the owner (`pengupool ask`): one of its choices, or a reply in the owner's words. */
function AgentAskRow({ a }: { a: Approval }): ReactElement {
  const app = useApp();
  const [reply, setReply] = useState("");
  const answer = (text: string): void => void app.call("ask.answer", { id: a.id, answer: text });
  const name = sessionName(app.sessions, a.session_id ?? "");
  const options = a.options ?? [];
  const stack = options.some((o) => [...o].length > 24);
  const left = a.expires_at !== undefined && a.expires_at !== "" ? age(new Date().toISOString(), Date.parse(a.expires_at)) : undefined;
  return (
    <WaitRow
      icon={<MessageCircleQuestion className="size-4 shrink-0 text-warning" />}
      label={`${name}: question`}
      who={
        <>
          <strong className="font-semibold">{name}</strong>
          <NeedRole id={a.session_id ?? ""} />
        </>
      }
      meta={teamName(app, a.team_id, a.session_id)}
      at={a.created_at}
      after={left === undefined ? undefined : left === "now" ? "expires now" : `expires in ${left}`}
    >
      <Clamp lines={4}>{a.detail}</Clamp>
      {options.length > 0 ? (
        <div className={stack ? "grid grid-cols-1 gap-1.5" : "flex flex-wrap justify-end gap-1.5"}>
          {options.map((o) => (
            <Button key={o} size="sm" variant="secondary" className={cn(OPTION, stack && "justify-start")} onClick={() => answer(o)}>
              {o}
            </Button>
          ))}
        </div>
      ) : null}
      <form
        className="flex gap-1.5"
        onSubmit={(e) => {
          e.preventDefault();
          if (reply.trim() !== "") answer(reply);
        }}
      >
        <Input value={reply} onChange={(e) => setReply(e.target.value)} placeholder="Or reply in your own words" aria-label="Reply" />
        <Button size="sm" type="submit" variant={options.length === 0 ? "default" : "secondary"} disabled={reply.trim() === ""}>
          Reply
        </Button>
      </form>
    </WaitRow>
  );
}

function LaunchRow({ a }: { a: Approval }): ReactElement {
  const app = useApp();
  const task = app.domain.tasks.find((t) => t.id === a.task_id);
  const repo = app.domain.repos.find((r) => r.id === task?.repo_id);
  return (
    <WaitRow icon={<Rocket className="size-4 shrink-0 text-primary" />} label={`${task?.title ?? "Team"}: team launch`} who={<strong className="font-semibold">{task?.title ?? "Team launch"}</strong>} meta={repo?.name} at={a.created_at}>
      <Clamp lines={4}>{a.detail}</Clamp>
      <Actions>
        <Button size="sm" variant="default" onClick={() => app.showDialog(<ProposalSheet taskId={a.task_id ?? ""} />)}>
          Review
        </Button>
      </Actions>
    </WaitRow>
  );
}

export function ProposalSheet({ taskId }: { taskId: string }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const task = app.domain.tasks.find((t) => t.id === taskId);
  const proposal = app.domain.proposals.filter((p) => p.task_id === taskId).reduce<Proposal | undefined>((best, p) => (best === undefined || p.version > best.version ? p : best), undefined);
  const approval = app.domain.approvals.find((a) => a.task_id === taskId && a.kind === "launch" && a.state === "pending");
  const [details, setDetails] = useState(false);
  const [members, setMembers] = useState<ProposedMember[]>(proposal?.members ?? []);
  const [busy, setBusy] = useState(false);
  useEffect(() => setMembers(proposal?.members ?? []), [proposal?.id]);
  if (task === undefined || proposal === undefined) {
    return (
      <Sheet title="Team launch" onClose={close}>
        <p className="text-label">This proposal is gone.</p>
      </Sheet>
    );
  }
  const repo = app.domain.repos.find((r) => r.id === task.repo_id);
  const edited = JSON.stringify(members) !== JSON.stringify(proposal.members);
  const launch = async (): Promise<void> => {
    if (approval === undefined) return;
    setBusy(true);
    const r = await app.call("approval.decide", {
      id: approval.id,
      decision: "approve",
      version: proposal.version,
    });
    setBusy(false);
    if (r !== undefined) close();
  };
  return (
    <Sheet
      title={`Launch “${task.title}”`}
      description={`${repo?.name ?? "?"} · ${task.harness} · proposal v${proposal.version}`}
      onClose={close}
      wide
      footer={
        <>
          <Button
            variant="ghost"
            onClick={async () => {
              if (approval !== undefined)
                await app.call("approval.decide", {
                  id: approval.id,
                  decision: "deny",
                });
              close();
            }}
          >
            Decline
          </Button>
          <Button variant="default" disabled={busy || edited || approval === undefined} onClick={() => void launch()}>
            <Rocket /> {busy ? "Launching…" : edited ? "Save edits first" : "Launch team"}
          </Button>
        </>
      }
    >
      <p className="text-body">{task.objective}</p>
      <div className="mt-4 grid grid-cols-1 gap-2">
        {members.map((m, i) => (
          <div key={i} className="flex items-center gap-2 rounded-md border border-border p-2.5">
            <span className="font-medium">{m.name}</span>
            {m.role === "main" ? (
              <RoleTag role="lead" />
            ) : m.role === "sme" ? (
              <RoleTag role="sme" />
            ) : (
              <>
                <RoleTag role="helper" /> {skillName(app.domain, m.skill_id)}
              </>
            )}
            {m.coordinator === true ? <Badge tone="warning">Coordinator</Badge> : null}
          </div>
        ))}
      </div>
      {(proposal.warnings ?? []).map((w) => (
        <p key={w} className="mt-2 rounded-md bg-warning/10 p-2 text-caption text-warning">
          {w}
        </p>
      ))}
      <button type="button" className="mt-3 text-label text-primary hover:underline" aria-expanded={details} onClick={() => setDetails(!details)}>
        {details ? "Hide details" : "Details: assignments, decision, edits"}
      </button>
      {details ? (
        <div className="mt-3 grid grid-cols-1 gap-3 rounded-lg border border-border p-3">
          <p className="text-caption text-muted-foreground">
            Decided by {proposal.provider}
            {proposal.model !== undefined ? ` · ${proposal.model}` : ""} · confidence {proposal.confidence.toFixed(2)}
            {proposal.fallback === true ? " · fallback" : ""}
            {(proposal.gaps ?? []).length > 0 ? ` · not covered: ${(proposal.gaps ?? []).join(", ")}` : ""}
          </p>
          {members.map((m, i) => (
            <div key={i} className="grid grid-cols-1 gap-1.5">
              <Label>
                {m.name} — assignment
                <Textarea value={m.assignment} onChange={(e) => setMembers(members.map((x, k) => (k === i ? { ...x, assignment: e.target.value } : x)))} />
              </Label>
              <div className="flex items-center gap-3 text-label">
                <label className="flex items-center gap-1.5">
                  <input type="radio" name="coordinator" checked={m.coordinator === true} onChange={() => setMembers(members.map((x, k) => ({ ...x, coordinator: k === i })))} />
                  Coordinator
                </label>
                <Select
                  className="h-7 w-auto"
                  value={m.harness ?? ""}
                  aria-label={`Agent for ${m.name}`}
                  onChange={(e) =>
                    setMembers(
                      members.map((x, k) =>
                        k === i
                          ? {
                              ...x,
                              harness: e.target.value === "" ? undefined : e.target.value,
                            }
                          : x,
                      ),
                    )
                  }
                >
                  {(() => {
                    const req = app.domain.skills.find((x) => x.id === m.skill_id)?.requires ?? [];
                    return req.length === 0 || req.includes(task.harness) ? <option value="">Team default ({harnessLabel(task.harness)})</option> : null;
                  })()}
                  {AGENTS.filter((a) => {
                    if (a.hidden) return false;
                    const req = app.domain.skills.find((x) => x.id === m.skill_id)?.requires ?? [];
                    return req.length === 0 || req.includes(a.id);
                  }).map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.label}
                    </option>
                  ))}
                </Select>
                {m.role === "helper" ? (
                  <Button size="sm" variant="ghost" onClick={() => setMembers(members.filter((_, k) => k !== i))}>
                    Remove helper
                  </Button>
                ) : null}
              </div>
            </div>
          ))}
          <Select
            value=""
            aria-label="Add helper"
            onChange={(e) => {
              const sk = app.domain.skills.find((s) => s.id === e.target.value);
              if (sk !== undefined)
                setMembers([
                  ...members,
                  {
                    skill_id: sk.id,
                    name: sk.name,
                    role: "helper",
                    assignment: `Assist with ${task.title}`,
                    harness: sk.harness === "" ? undefined : sk.harness,
                  },
                ]);
            }}
          >
            <option value="">Add a helper…</option>
            {app.domain.skills
              .filter((s) => !members.some((m) => m.skill_id === s.id))
              .map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
          </Select>
          <div className="flex gap-2">
            <Button
              size="sm"
              disabled={!edited}
              onClick={() =>
                void app.call("proposal.edit", {
                  task_id: task.id,
                  version: proposal.version,
                  members,
                })
              }
            >
              Save as v{proposal.version + 1}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => void app.call("task.repropose", { id: task.id })}>
              Propose again
            </Button>
          </div>
        </div>
      ) : null}
    </Sheet>
  );
}

interface Preview {
  team: string;
  member: Member;
  worktree?: string;
  worktree_removable?: boolean;
  worktree_state?: string;
  skill?: string;
}

export function CloseHelperSheet({ team, member }: { team: Team; member: Member }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const [p, setP] = useState<Preview | null>(null);
  const [keep, setKeep] = useState(false);
  const [reason, setReason] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    void app
      .call<Preview>("team.close_preview", {
        team_id: team.id,
        session_id: member.session_id,
      })
      .then((r) => {
        if (r !== undefined) setP(r);
      });
  }, [team.id, member.session_id, n]);
  return (
    <Sheet
      title={`Remove ${member.name}`}
      description={`Team ${team.name}. A formation change: approving it closes the member. Its own experience stays with it; the team's record keeps that it left and why, and the work it did.`}
      onClose={close}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant="default"
            disabled={p === null || (p.worktree_removable === false && !keep)}
            onClick={async () => {
              const r = await app.call("team.close_helper", {
                team_id: team.id,
                session_id: member.session_id,
                keep_worktree: keep,
                reason,
              });
              if (r !== undefined) close();
              else setN(n + 1);
            }}
          >
            Approve and remove
          </Button>
        </>
      }
    >
      {p === null ? (
        <p className="text-label text-faint">Loading…</p>
      ) : (
        <div className="grid grid-cols-1 gap-3 text-label">
          <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why it leaves (kept in the team's record; optional)" aria-label="Reason" />
          <div className="rounded-md border border-border p-3">
            <p className="font-mono text-caption">{p.worktree ?? "no worktree"}</p>
            <p className="mt-1 text-muted-foreground">
              {p.worktree_state}
              {p.worktree_removable === true ? " — will be removed; its branch is kept." : " — cannot be removed without losing work."}
            </p>
            {p.worktree_removable === false ? (
              <label className="mt-2 flex items-center gap-2">
                <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} /> Keep the worktree on disk and close the helper anyway
              </label>
            ) : null}
          </div>
        </div>
      )}
    </Sheet>
  );
}
