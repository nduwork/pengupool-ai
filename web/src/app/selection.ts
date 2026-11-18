/** How long (ms) to keep waiting for a just-selected session to appear in the list; 0: fall back now. */
export const SELECT_WAIT_MS = 5000;

/** The time left to wait for `selected`, when it is the session `select` was last called with. */
export function selectionWait(just: { id: string; at: number } | null, selected: string | null, now: number): number {
  return selected !== null && just?.id === selected ? Math.max(0, SELECT_WAIT_MS - (now - just.at)) : 0;
}
