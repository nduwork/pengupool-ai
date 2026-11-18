// Restart all (run.restartAll): the pure parts the sheet, Settings › Agents and the map share.

export interface RestartItem {
  session_id: string;
  name: string;
  harness: string;
  state: string; // queued | restarting | resumed | fresh | failed | cancelled
  reason?: string;
}

type Tone = "neutral" | "primary" | "success" | "warning" | "danger" | "info"; // Badge's tones
type Running = { id: string; state: string; status?: string; harness?: string; node_id?: string };

/** On this Mac: the host restarts only its own sessions, never one on an SSH host. */
export function onThisMac(s: { node_id?: string }, nodeID: string): boolean {
  return s.node_id === undefined || s.node_id === "" || s.node_id === nodeID;
}

/** Mid-turn, waiting on the owner, or still starting: Restart all waits for it. */
export function busyNow(s: { status?: string }): boolean {
  return s.status === "working" || s.status === "approval" || s.status === "starting";
}

/** Some agent session is running on this Mac (nodeID: the host's own, from its hello). */
export function agentsRunning(sessions: Running[], isAgent: (s: Running) => boolean, nodeID: string): boolean {
  return sessions.some((s) => s.state === "running" && isAgent(s) && onThisMac(s, nodeID));
}

/** The running agent sessions, in order; waits: it restarts only once its turn ends. */
export function restartPlan<S extends Running>(sessions: S[], isAgent: (s: S) => boolean, nodeID: string): Array<{ s: S; waits: boolean }> {
  return sessions.filter((s) => s.state === "running" && isAgent(s) && onThisMac(s, nodeID)).map((s) => ({ s, waits: busyNow(s) }));
}

/** Some item is queued or restarting: the run is going. */
export function restartActive(items: RestartItem[]): boolean {
  return items.some((it) => it.state === "queued" || it.state === "restarting");
}

export function restartLabel(state: string, busy = false): { label: string; tone: Tone } {
  switch (state) {
    case "queued":
      return { label: busy ? "waits for its turn to end" : "waits its turn", tone: "info" };
    case "restarting":
      return { label: "restarting", tone: "primary" };
    case "resumed":
      return { label: "resumed", tone: "success" };
    case "fresh":
      return { label: "restarted fresh", tone: "warning" };
    case "failed":
      return { label: "failed", tone: "danger" };
    case "cancelled":
      return { label: "cancelled", tone: "neutral" };
  }
  return { label: state, tone: "neutral" };
}

/** "2 resumed · 1 restarted fresh · 1 waiting" */
export function restartSummary(items: RestartItem[]): string {
  if (items.length === 0) return "No agent session is running.";
  const out: string[] = [];
  for (const st of ["resumed", "fresh", "restarting", "queued", "failed", "cancelled"]) {
    const n = items.filter((it) => it.state === st).length;
    if (n > 0) out.push(`${n} ${st === "queued" ? "waiting" : restartLabel(st).label}`);
  }
  return out.join(" · ");
}

/** Opens Settings › Agents, where a run is followed. */
export function followInSettings(setNav: (n: "settings") => void): void {
  try {
    localStorage.setItem("pengupool.settings.section", JSON.stringify("agents"));
  } catch {
    /* no storage: Settings opens on its remembered section */
  }
  setNav("settings");
}
