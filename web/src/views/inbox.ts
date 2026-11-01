import type { SessionView } from "../protocol/control";
import type { Approval, TeamMessage } from "../protocol/domain";
import { type SpotCheck, sessionStatus } from "../app/ctx";
import { openAsks } from "./map/asks";

/** A gate waiting on the owner (the host's gates.open): a check-in or a permission prompt. */
export interface OpenGate {
  id: number;
  session_id: string;
  agent: string;
  kind: "checkin" | "permission";
  question: string;
  /** The agent's own line for a permission prompt's call, when it gave one. */
  description?: string;
  floor: boolean;
  floor_hit?: string;
  /** A check-in's answer from the Compass, with what it rests on; the owner confirms it by sending it. */
  suggestion?: string;
  evidence?: string;
  created_at?: string;
  team_id?: string;
}

/** One live ask of the owner: shown in the panel while it lasts, gone once answered. */
export interface Need {
  /** The session it is about: one need per session (or per key). */
  session: string;
  /** Set when one session can have more than one: an ask is keyed ask:{message id}. */
  key?: string;
  name: string;
  kind: "checkin" | "permission" | "block" | "ask";
  text: string;
  /** A permission prompt's whole call, shown on request: the text leads with what it is for. */
  detail?: string;
  /** The gate behind a check-in (to answer it) or a permission prompt (floor flag). */
  gate?: OpenGate;
  /** An ask triage could not settle: the message, and the member it waits on. */
  ask?: TeamMessage;
  toName?: string;
}

/** The asks triage could not settle: escalated, still open, the asker still running. */
export function askNeeds(messages: TeamMessage[], sessions: SessionView[]): Need[] {
  const out: Need[] = [];
  for (const a of openAsks(messages)) {
    const from = sessions.find((s) => s.id === a.from);
    if (a.escalated_at === undefined || from === undefined || from.state !== "running") continue;
    const to = sessions.find((s) => s.id === a.to);
    out.push({ session: a.from, key: `ask:${a.id}`, name: from.name, kind: "ask", text: a.text, ask: a, toName: to?.name ?? a.to.slice(0, 8) });
  }
  return out;
}

const keyOf = (n: Need): string => n.key ?? n.session;

/**
 * A permission prompt in a line: the tool and the agent's description of the call, with the floor
 * item it touches; without a description, the call's first line, clipped. The whole call is the
 * Need's detail.
 */
export function permissionText(g: OpenGate): string {
  const [tool, ...rest] = g.question.split(": ");
  const call = rest.join(": ");
  let what = g.description?.trim() ?? "";
  if (what === "") {
    const first = (call === "" ? g.question : call).split("; ")[0] ?? "";
    what = first.length > 80 ? `${first.slice(0, 79)}…` : first;
  }
  const floor = g.floor && g.floor_hit !== undefined && g.floor_hit !== "" ? ` (${g.floor_hit})` : "";
  return `Approve ${call === "" ? "" : `${tool}: `}${what}${floor}?`;
}


/**
 * What needs the owner now, one ask per running session: a lost connection, else a first-run dialog
 * it is stuck on while starting, else a permission prompt (the exact tool call when the host recorded
 * it), else a check-in awaiting an answer.
 */
export function sessionNeeds(sessions: SessionView[], gates: OpenGate[]): Need[] {
  const out: Need[] = [];
  for (const s of sessions) {
    if (s.state !== "running") continue;
    const st = sessionStatus(s);
    const mine = gates.filter((g) => g.session_id === s.id);
    const perm = mine.find((g) => g.kind === "permission");
    const checkin = mine.find((g) => g.kind === "checkin");
    const n = { session: s.id, name: s.name };
    if (st === "lost" || st === "unreachable") out.push({ ...n, kind: "block", text: `is ${st}` });
    else if (s.waiting) out.push({ ...n, kind: "block", text: `is waiting on a first-run dialog (${s.waiting}): answer it in its terminal` });
    else if (st === "approval")
      out.push(perm !== undefined ? { ...n, kind: "permission", text: permissionText(perm), detail: perm.question, gate: perm } : { ...n, kind: "permission", text: "is waiting on a permission prompt" });
    else if (checkin !== undefined && st !== "working") out.push({ ...n, kind: "checkin", text: checkin.question, gate: checkin });
  }
  return out;
}

/** A pending approval's notification title, by its kind: an agent's question is not a team launch. */
export function approvalTitle(kind: string): string {
  return kind === "ask" ? "An agent asks you" : kind === "formation" ? "Team change to approve" : "Team launch ready";
}

/** One thing waiting on the owner: a pending approval or a need. key is stable while it waits. */
export interface Waiting {
  key: string;
  title: string;
  body: string;
  session?: string;
}

/**
 * Everything waiting on the owner, the one list the badge, the bell and its panel show: pending
 * approvals (an agent's ask only while the agent runs; a permission is a need instead), then what
 * each running session needs, then the asks triage could not settle, then the go-aheads to check. An item leaves the list as soon
 * as the state says it was answered, wherever it was answered.
 */
export function waitingOnOwner(approvals: Approval[], sessions: SessionView[], messages: TeamMessage[], gates: OpenGate[], spots: SpotCheck[] = []): { approvals: Approval[]; needs: Need[]; items: Waiting[] } {
  const running = (id?: string): boolean => sessions.some((s) => s.id === id && s.state === "running");
  const pending = approvals.filter((a) => a.state === "pending" && a.kind !== "permission" && (a.kind !== "ask" || running(a.session_id)));
  const needs = [...sessionNeeds(sessions, gates), ...askNeeds(messages, sessions)];
  const words = (n: Need): string => (n.kind === "checkin" ? `${n.name} asks: ${n.text}` : n.kind === "ask" ? `${n.name} asked ${n.toName ?? ""}: ${n.text}` : `${n.name} ${n.text}`);
  const items: Waiting[] = [
    ...pending.map((a) => ({ key: `approval:${a.id}`, title: approvalTitle(a.kind), body: a.detail ?? "Review the proposal", session: a.session_id })),
    ...needs.map((n) => ({ key: `need:${keyOf(n)}:${n.kind}`, title: `${n.name} needs you`, body: words(n), session: n.session })),
    ...spots.map((g) => ({ key: `spot:${g.id}`, title: "Was this go-ahead right?", body: `${g.agent}: ${g.question}` })),
  ];
  return { approvals: pending, needs, items };
}
