import { Bell, MessageCircleQuestion, ShieldQuestion, Rocket, ThumbsDown, ThumbsUp, Users } from "lucide-react";
import { Popover } from "radix-ui";
import { useEffect, useState, type ReactElement } from "react";

import { AGENTS, ago, harnessLabel, sessionName, skillName, useApp, usePersisted } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Badge, Input, Label, Select, Textarea, submitOnEnter } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { Approval, Member, Proposal, ProposedMember, Team } from "../protocol/domain";
import { FormationSheet } from "./Formation";
import { reconcileInbox, sessionNeeds, type InboxEntry, type Need } from "./inbox";

// Urgent approvals stay visible: a bell with a count in the title bar. NeedsYouBar is what needs the
// owner now: an agent's check-in (answered here: Go on, Stop, or a reply typed into its terminal for
// it), a permission dialog (it lives in the harness, so Open terminal) and the go-aheads triage picked
// for the owner to check (a one-tap verdict). The bell's
// popover also holds the Inbox: the history of those asks, one entry per session.

export function ApprovalsBell(): ReactElement {
  const app = useApp();
  const pending = app.domain.approvals.filter((a) => a.state === "pending" && a.kind !== "permission"); // permission requests are answered in the terminal
  const [inbox, setInbox] = usePersisted<InboxEntry[]>("inbox", []);
  useEffect(() => setInbox(reconcileInbox(inbox, sessionNeeds(app.sessions, app.gates), Date.now())), [app.sessions, app.gates]);
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <Button size="icon" variant="ghost" aria-label={`${pending.length} pending approvals`} className="relative">
          <Bell />
          {pending.length > 0 ? (
            <span className="absolute -right-0.5 -top-0.5 grid grid-cols-1 min-w-4 place-items-center rounded-full bg-warning px-1 text-micro font-bold text-black">{pending.length}</span>
          ) : null}
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} className="z-50 w-96 rounded-lg border border-border bg-raised p-2 shadow-2xl">
          <p className="px-2 pb-1 text-label font-semibold">Approvals</p>
          {pending.length === 0 ? <p className="px-2 py-3 text-label text-muted-foreground">Nothing is waiting for you.</p> : null}
          <div className="grid grid-cols-1 gap-1">
            {pending.map((a) => (a.kind === "formation" ? <FormationRow key={a.id} a={a} /> : <LaunchRow key={a.id} a={a} />))}
          </div>
          {inbox.length > 0 ? (
            <>
              <p className="px-2 pb-1 pt-2 text-label font-semibold">Inbox</p>
              {inbox.map((e) => (
                <button key={e.session} type="button" onClick={() => app.select(e.session)} className="flex w-full items-center gap-2 rounded-md p-2 text-left text-label hover:bg-hover">
                  <ShieldQuestion className={e.resolvedAt === undefined ? "size-4 shrink-0 text-warning" : "size-4 shrink-0 text-faint"} />
                  <span className={e.resolvedAt === undefined ? "min-w-0 flex-1 truncate" : "min-w-0 flex-1 truncate text-muted-foreground"}>
                    <strong className="font-semibold">{e.name}</strong> {e.text}
                  </span>
                  <span className="shrink-0 text-caption text-faint">{e.resolvedAt === undefined ? ago(new Date(e.at).toISOString()) : "resolved"}</span>
                </button>
              ))}
            </>
          ) : null}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** The go-aheads waiting for the owner's 👍/👎, refreshed every 30 s; shared by the Needs-you bar and the Compass panel. */
export function useSpotChecks(): { spots: { id: number; agent: string; question: string }[]; verdict: (id: number, right: boolean) => Promise<void> } {
  const app = useApp();
  const [spots, setSpots] = useState<{ id: number; agent: string; question: string }[]>([]);
  const loadSpots = (): void => void app.call<typeof spots>("compass.spot_checks", {}).then((r) => setSpots(r ?? []));
  useEffect(() => {
    loadSpots();
    const t = window.setInterval(loadSpots, 30_000);
    return () => window.clearInterval(t);
  }, []);
  const verdict = (id: number, right: boolean): Promise<void> => app.call("compass.spot_check", { gate_id: id, right }).then(loadSpots);
  return { spots, verdict };
}

/** Sessions blocked on a permission prompt: the prompt lives in their terminal, so offer to open it. */
export function NeedsYouBar(): ReactElement | null {
  const app = useApp();
  const list = sessionNeeds(app.sessions, app.gates);
  const { spots, verdict } = useSpotChecks();
  if (list.length === 0 && spots.length === 0) return null;
  return (
    <div className="grid grid-cols-1 gap-1 border-b border-warning/30 bg-warning/10 px-3 py-1.5" role="region" aria-label="Needs you">
      {list.map((n) => (
        <NeedRow key={n.session} n={n} />
      ))}
      {spots.map((g) => (
        <div key={g.id} className="flex items-center gap-2 text-label">
          <ThumbsUp className="size-4 shrink-0 text-warning" />
          <span className="min-w-0 flex-1 truncate">
            Was this go-ahead right? <strong className="font-semibold">{g.agent}</strong>: {g.question}
          </span>
          <Button size="sm" variant="default" aria-label="Right" onClick={() => verdict(g.id, true)}>
            <ThumbsUp />
          </Button>
          <Button size="sm" aria-label="Wrong" onClick={() => verdict(g.id, false)}>
            <ThumbsDown />
          </Button>
        </div>
      ))}
    </div>
  );
}

/** One ask of the owner. A check-in is answered here; a permission dialog lives in the harness's terminal. */
function NeedRow({ n }: { n: Need }): ReactElement {
  const app = useApp();
  const [reply, setReply] = useState(false);
  const [showCall, setShowCall] = useState(false);
  const [text, setText] = useState("");
  const [confirming, setConfirming] = useState(false);
  const answer = async (t: string): Promise<void> => {
    if ((await app.call("checkin.answer", { gate_id: n.gate?.id, text: t })) !== undefined) setReply(false);
  };
  const checkin = n.kind === "checkin";
  return (
    <div className="grid grid-cols-1 gap-1 text-label">
      <div className="flex items-center gap-2">
        {checkin ? <MessageCircleQuestion className="size-4 shrink-0 text-warning" /> : <ShieldQuestion className="size-4 shrink-0 text-warning" />}
        <span className="min-w-0 flex-1 truncate" title={n.text}>
          <strong className="font-semibold">{n.name}</strong> {checkin ? "asks: " : ""}
          {n.text}
        </span>
        {n.gate?.floor === true ? <Badge tone="danger">floor</Badge> : null}
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
        {n.detail !== undefined ? (
          <Button size="sm" variant="ghost" aria-expanded={showCall} onClick={() => setShowCall(!showCall)}>
            {showCall ? "Hide command" : "Show command"}
          </Button>
        ) : null}
        <Button size="sm" variant={checkin ? "ghost" : "default"} title={checkin ? undefined : "The permission dialog is in the agent's terminal"} onClick={() => app.select(n.session)}>
          Open terminal
        </Button>
      </div>
      {checkin && n.gate?.suggestion !== undefined && n.gate.suggestion !== "" ? (
        <div className="flex items-center gap-2 pl-6 text-caption text-muted-foreground">
          <span className="min-w-0 flex-1 truncate" title={n.gate.evidence}>
            Suggested: “{n.gate.suggestion}”{n.gate.evidence !== undefined && n.gate.evidence !== "" ? ` — ${n.gate.evidence}` : ""}
          </span>
          <Button
            size="sm"
            variant="default"
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
      {showCall && n.detail !== undefined ? <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-hover p-2 font-mono text-caption">{n.detail}</pre> : null}
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
    </div>
  );
}

/** A member asked to change who is on its team: nothing changes until the owner approves. */
function FormationRow({ a }: { a: Approval }): ReactElement {
  const app = useApp();
  const decide = (decision: string): void => void app.call("approval.decide", { id: a.id, decision });
  const by = a.change?.requested_by !== undefined && a.change.requested_by !== "" ? sessionName(app.sessions, a.change.requested_by) : "";
  // Adding a member starts an agent: the owner picks which one and its model before approving.
  const team = app.domain.teams.find((t) => t.id === a.team_id);
  return (
    <div className="grid gap-2 rounded-md p-2 hover:bg-hover">
      <div className="flex min-w-0 items-start gap-2">
        <Users className="mt-0.5 size-4 shrink-0 text-primary" />
        <span className="min-w-0 text-label">
          {by !== "" ? <strong>{by} asks: </strong> : null}
          {a.detail}
        </span>
      </div>
      <div className="flex justify-end gap-1.5">
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
      </div>
    </div>
  );
}

function LaunchRow({ a }: { a: Approval }): ReactElement {
  const app = useApp();
  return (
    <div className="flex items-center gap-2 rounded-md p-2 hover:bg-hover">
      <Rocket className="size-4 shrink-0 text-primary" />
      <span className="min-w-0 flex-1 truncate text-label">{a.detail}</span>
      <Button size="sm" variant="default" onClick={() => app.showDialog(<ProposalSheet taskId={a.task_id ?? ""} />)}>
        Review
      </Button>
    </div>
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
            {m.role === "main" ? <Badge tone="primary">Main worker</Badge> : <Badge>Helper · {skillName(app.domain, m.skill_id)}</Badge>}
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
            <option value="">Add a helper agent…</option>
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
