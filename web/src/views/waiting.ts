// What a team member row says about a session that waits on something. Pure, so they can be tested.

/** What a member row says while its session is stuck on a first-run dialog; undefined when it is not. */
export const waitingNote = (waiting?: string): string | undefined => (waiting !== undefined && waiting !== "" ? `waiting on ${waiting}` : undefined);

/** Agents with an open check-in that are not working. */
export const checkinWaiters = (ids: string[], gates: { session_id: string; kind: string }[], statusOf: (id: string) => string): string[] =>
  ids.filter((id) => statusOf(id) !== "working" && gates.some((g) => g.session_id === id && g.kind === "checkin"));
