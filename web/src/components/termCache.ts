/** Terminal scrollback is a per-viewer setting, in lines. Each line costs about 12 bytes per column in xterm.js. */
export const SCROLLBACK_KEY = "pengupool.terminal.scrollback";
export const SCROLLBACK_DEFAULT = 20000;
export const SCROLLBACK_MIN = 1000;
export const SCROLLBACK_MAX = 100000;

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
 * The terminals of sessions the user has left, most recent last. Selecting a session again takes its
 * terminal back and resumes from the worker's replay ring, so the scrollback it had survives the switch.
 * The least recently left one is disposed when more than `max` are kept: this bounds the memory.
 */
export class TermCache<V> {
  private readonly map = new Map<string, V>();

  constructor(
    private readonly max: number,
    private readonly dispose: (v: V) => void,
  ) {}

  /** Removes and returns the kept terminal of a session. */
  take(id: string): V | undefined {
    const v = this.map.get(id);
    this.map.delete(id);
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

  /** Drops a session's kept terminal for good (its session was removed). */
  drop(id: string): void {
    const v = this.map.get(id);
    this.map.delete(id);
    if (v !== undefined) this.dispose(v);
  }

  get size(): number {
    return this.map.size;
  }
}
