import type { SessionView } from "../protocol/control";

/** When the owner last looked at each session, by id (ISO time). */
export type Viewed = Record<string, string>;

/** Output arrived since the owner last viewed the session; a session first seen has no baseline and reads as read. */
export function isUnread(s: SessionView, viewed: Viewed): boolean {
  const at = viewed[s.id];
  return at !== undefined && s.active_at !== undefined && s.active_at > at;
}

/** The viewed times after a state update: the selected session is read up to now, and a new one starts at now. */
export function markViewed(viewed: Viewed, sessions: SessionView[], selected: string | null, now: string): Viewed {
  const next: Viewed = {};
  for (const s of sessions) next[s.id] = s.id === selected ? now : (viewed[s.id] ?? now);
  const same = Object.keys(next).length === Object.keys(viewed).length && Object.entries(next).every(([k, v]) => viewed[k] === v);
  return same ? viewed : next;
}
