// The session list state: one snapshot followed by revisioned deltas, exactly as the daemon
// describes it in docs/protocol/v1.md section 6.
import { T, type ControlMessage, type SessionView } from "../protocol/control";
import { EMPTY_DOMAIN, type Domain } from "../protocol/domain";

export interface SessionsView {
  rev: number;
  epoch: string;
  sessions: SessionView[];
  domain: Domain;
}

function byName(a: SessionView, b: SessionView): number {
  // Live sessions first; exited and lost ones stay visible but out of the way.
  const rank = (a.state === "running" ? 0 : 1) - (b.state === "running" ? 0 : 1);
  return rank !== 0 ? rank : a.name.localeCompare(b.name);
}

export class SessionsStore {
  private rev = 0;
  private epoch = "";
  private readonly byId = new Map<string, SessionView>();
  private domain: Domain = EMPTY_DOMAIN;
  private readonly listeners = new Set<(view: SessionsView) => void>();
  private readonly epochListeners = new Set<() => void>();

  /** Applies a control message; returns true when the daemon epoch changed. */
  apply(m: ControlMessage): boolean {
    switch (m.type) {
      case T.StateSnapshot: {
        const epochChanged = this.epoch !== "" && m.epoch !== undefined && m.epoch !== this.epoch;
        this.rev = m.rev ?? 0;
        if (m.epoch !== undefined) this.epoch = m.epoch;
        this.byId.clear();
        for (const s of m.sessions ?? []) this.byId.set(s.id, s);
        if (m.domain !== undefined) this.domain = normalizeDomain(m.domain);
        this.publish();
        if (epochChanged) {
          // The retained screen state belongs to a daemon that is gone; expect a fresh snapshot.
          for (const fn of this.epochListeners) fn();
        }
        return epochChanged;
      }
      case T.StateDelta: {
        if (m.epoch !== undefined && m.epoch !== this.epoch) {
          this.epoch = m.epoch;
          this.rev = 0;
          for (const fn of this.epochListeners) fn();
          return true;
        }
        this.rev = m.rev ?? this.rev;
        for (const s of m.upserted ?? []) this.byId.set(s.id, s);
        for (const id of m.removed ?? []) this.byId.delete(id);
        if (m.domain !== undefined) this.domain = normalizeDomain(m.domain);
        this.publish();
        return false;
      }
      default:
        return false;
    }
  }

  get currentRev(): number {
    return this.rev;
  }

  get currentEpoch(): string {
    return this.epoch;
  }

  get(id: string): SessionView | undefined {
    return this.byId.get(id);
  }

  list(): SessionView[] {
    return [...this.byId.values()].sort(byName);
  }

  subscribe(fn: (view: SessionsView) => void): () => void {
    this.listeners.add(fn);
    fn(this.view());
    return () => this.listeners.delete(fn);
  }

  /** Fires when the daemon epoch changes, so live attachments must be rebuilt. */
  onEpochChange(fn: () => void): () => void {
    this.epochListeners.add(fn);
    return () => this.epochListeners.delete(fn);
  }

  private view(): SessionsView {
    return { rev: this.rev, epoch: this.epoch, sessions: this.list(), domain: this.domain };
  }

  private publish(): void {
    const v = this.view();
    for (const fn of this.listeners) fn(v);
  }
}

/** Go encodes empty slices as null; the UI wants arrays. */
function normalizeDomain(d: Domain): Domain {
  return {
    repos: d.repos ?? [],
    skills: d.skills ?? [],
    tasks: d.tasks ?? [],
    proposals: d.proposals ?? [],
    teams: (d.teams ?? []).map((t) => ({ ...t, members: t.members ?? [] })),
    approvals: d.approvals ?? [],
    messages: d.messages ?? [],
    nodes: d.nodes ?? [],
    update: d.update,
  };
}
