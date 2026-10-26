/** Terminal scrollback is a per-viewer setting, in lines. Each line costs about 12 bytes per column in xterm.js. */
export const SCROLLBACK_KEY = "pengupool.terminal.scrollback";
export const SCROLLBACK_DEFAULT = 20000;
export const SCROLLBACK_MIN = 1000;
export const SCROLLBACK_MAX = 50000;

export function clampScrollback(n: number): number {
  return Number.isFinite(n) ? Math.min(SCROLLBACK_MAX, Math.max(SCROLLBACK_MIN, Math.round(n))) : SCROLLBACK_DEFAULT;
}

export function scrollbackSetting(): number {
  try {
    const raw = localStorage.getItem(SCROLLBACK_KEY);
    return raw === null ? SCROLLBACK_DEFAULT : clampScrollback(JSON.parse(raw) as number);
  } catch {
    return SCROLLBACK_DEFAULT;
  }
}

/**
 * Names the process a session's output came from: the daemon's epoch, and the worker's pid and start.
 * A resumed session keeps its id but gets a new worker whose offsets restart, and a restarted daemon
 * starts new workers: a kept terminal is only resumed from the ring while this is unchanged.
 */
export function instanceOf(epoch: string, s: { pid?: number; started_at?: string }): string {
  return `${epoch}|${s.pid ?? 0}|${s.started_at ?? ""}`;
}

/**
 * The terminals of sessions the user has left, most recent last. Selecting a session again takes its
 * terminal back and resumes from the worker's replay ring, so the scrollback it had survives the switch.
 * The least recently left one is disposed when more than `max` are kept: this bounds the memory.
 */
export class TermCache<V extends { instance: string }> {
  private readonly map = new Map<string, V>();

  constructor(
    private readonly max: number,
    private readonly dispose: (v: V) => void,
  ) {}

  /**
   * Removes and returns the kept terminal of a session, when it was kept from the same process as
   * `instance`; one kept from another (the session was resumed, the daemon restarted) is disposed.
   */
  take(id: string, instance: string): V | undefined {
    const v = this.map.get(id);
    this.map.delete(id);
    if (v !== undefined && v.instance !== instance) {
      this.dispose(v);
      return undefined;
    }
    return v;
  }

  put(id: string, v: V): void {
    const old = this.map.get(id);
    if (old !== undefined && old !== v) this.dispose(old);
    this.map.delete(id);
    this.map.set(id, v);
    while (this.map.size > this.max) {
      const [oldest] = this.map.keys();
      const gone = this.map.get(oldest);
      this.map.delete(oldest);
      if (gone !== undefined) this.dispose(gone);
    }
  }

  /** Disposes the kept terminals of sessions that are gone from the list (closed or removed). */
  prune(live: ReadonlySet<string>): void {
    for (const [id, v] of [...this.map]) {
      if (live.has(id)) continue;
      this.map.delete(id);
      this.dispose(v);
    }
  }

  get size(): number {
    return this.map.size;
  }
}
