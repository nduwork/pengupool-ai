import type { TeamMessage } from "../../protocol/domain";

/** Mirrors the host's askOpen: an ask is open until its recipient sends the asker anything, or a
 *  message answers it by reply_to, or the owner or triage settled it. */
export function openAsks(ms: TeamMessage[]): TeamMessage[] {
  return ms.filter((a, i) => a.type === "ask" && a.settled_at === undefined && a.state !== "failed" && !ms.slice(i + 1).some((m) => m.reply_to === a.id || (m.from === a.to && m.to === a.from)));
}

/** An age as the pills show it: now, 4m, 59m, 1h, rounded down. */
export function age(at: string, now: number): string {
  const min = Math.floor((now - Date.parse(at)) / 60000);
  if (min < 1) return "now";
  return min < 60 ? `${min}m` : `${Math.floor(min / 60)}h`;
}

/** The pill for open asks on one edge or card: count and oldest age, ↑ once any escalated. */
export function askPill(asks: TeamMessage[], now: number): { text: string; escalated: boolean } {
  const oldest = asks.reduce((o, a) => (Date.parse(a.at) < Date.parse(o.at) ? a : o));
  const escalated = asks.some((a) => a.escalated_at !== undefined);
  const head = asks.length > 1 ? `? ${asks.length} · ${age(oldest.at, now)}` : `? ${age(oldest.at, now)}`;
  return { text: escalated ? `${head} ↑` : head, escalated };
}

/** A message's type as a glyph and a word; no type reads as tell. */
export const TYPE_CHIP: Record<string, { text: string; className: string }> = {
  tell: { text: "· tell", className: "text-muted-foreground" },
  ask: { text: "? ask", className: "font-semibold text-foreground" },
  done: { text: "✓ done", className: "text-success" },
  blocked: { text: "■ blocked", className: "text-danger" },
};

export const STATE_CELL: Record<string, { text: string; className: string }> = {
  queued: { text: "◷ queued", className: "text-muted-foreground" },
  delivered: { text: "✓ delivered", className: "text-muted-foreground" },
  failed: { text: "✕ failed", className: "text-danger" },
};

/** The newest message between each pair, keyed "from>to" in the route's own direction: a queued
 *  one draws the line dashed until it arrives. */
export function newestByPair(ms: TeamMessage[]): Map<string, TeamMessage> {
  const out = new Map<string, TeamMessage>();
  for (const m of ms) if (m.from !== "") out.set(`${m.from}>${m.to}`, m);
  return out;
}
