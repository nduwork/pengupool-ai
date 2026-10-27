import type { SessionView } from "../protocol/control";
import { sessionStatus } from "../app/ctx";

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
}

/** One live ask of the owner: shown in the bar while it lasts, and kept in the inbox after. */
export interface Need {
  /** The session it is about: the inbox merges by it. */
  session: string;
  name: string;
  kind: "checkin" | "permission" | "block";
  text: string;
  /** A permission prompt's whole call, shown on request: the text leads with what it is for. */
  detail?: string;
  /** The gate behind a check-in (to answer it) or a permission prompt (floor flag). */
  gate?: OpenGate;
}

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

export interface InboxEntry extends Need {
  at: number;
  /** Set when the ask went away. */
  resolvedAt?: number;
}

/** Resolved entries leave the inbox after this long (auto-archive). */
export const ARCHIVE_MS = 24 * 3600 * 1000;

/**
 * What needs the owner now, one ask per running session: a lost connection, else a permission
 * prompt (the exact tool call when the host recorded it), else a check-in awaiting an answer.
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
    else if (st === "approval")
      out.push(perm !== undefined ? { ...n, kind: "permission", text: permissionText(perm), detail: perm.question, gate: perm } : { ...n, kind: "permission", text: "is waiting on a permission prompt" });
    else if (checkin !== undefined && st !== "working") out.push({ ...n, kind: "checkin", text: checkin.question, gate: checkin });
  }
  return out;
}

/**
 * The inbox after a state update: one entry per session, merged, newest ask winning. A live need
 * opens (or reopens) its entry; an entry whose need went away is resolved and archived after a day.
 * A block stays while the session is still blocked, because it stays a live need.
 */
export function reconcileInbox(entries: InboxEntry[], live: Need[], now: number): InboxEntry[] {
  const by = new Map(entries.map((e) => [e.session, e]));
  for (const n of live) {
    const old = by.get(n.session);
    by.set(n.session, old !== undefined && old.resolvedAt === undefined && old.text === n.text && old.kind === n.kind ? old : { ...n, at: now });
  }
  const liveIds = new Set(live.map((n) => n.session));
  return [...by.values()]
    .map((e) => (liveIds.has(e.session) || e.resolvedAt !== undefined ? e : { ...e, resolvedAt: now }))
    .filter((e) => e.resolvedAt === undefined || now - e.resolvedAt < ARCHIVE_MS)
    .sort((a, b) => b.at - a.at);
}
