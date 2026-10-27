import { createContext, useContext, useState, type ReactElement, type ReactNode } from "react";

import type { Connection } from "../client/connection";
import type { SessionView } from "../protocol/control";
import type { Domain, Skill, Team } from "../protocol/domain";
import type { OpenGate } from "../views/inbox";
import { cn } from "../lib/utils";

export type Nav = "workspace" | "skills" | "teams" | "settings" | "ai";

/** An editor tab in the upper-centre pane: a file preview or a diff (the map is always tab 0). */
export interface EditorTab {
  key: string;
  kind: "file" | "diff";
  sessionId: string;
  path: string;
  title: string;
  staged?: boolean;
}

export interface AppCtx {
  conn: Connection;
  connected: boolean;
  sessions: SessionView[];
  /** Every session; only for resolving a selected terminal. */
  allSessions: SessionView[];
  domain: Domain;
  selected: string | null;
  /** Sessions with output since the owner last viewed them. */
  unread: ReadonlySet<string>;
  /** The gates waiting on the owner (check-ins and permission prompts); a host without an agent database has none. */
  gates: OpenGate[];
  select: (id: string | null) => void;
  nav: Nav;
  setNav: (n: Nav) => void;
  /** Runs a pool request and reports a failure as a toast; resolves undefined on failure. */
  call: <R = unknown>(type: string, params?: unknown) => Promise<R | undefined>;
  openTab: (t: EditorTab) => void;
  showDialog: (d: ReactNode) => void;
}

export const Ctx = createContext<AppCtx | null>(null);

export function useApp(): AppCtx {
  const c = useContext(Ctx);
  if (c === null) throw new Error("useApp outside the app");
  return c;
}

export function sessionName(sessions: SessionView[], id: string): string {
  return sessions.find((s) => s.id === id)?.name ?? id.slice(0, 8);
}

export function teamOf(domain: Domain, sessionId: string): Team | undefined {
  return domain.teams.find(
    (t) => t.state === "active" && t.members.some((m) => m.session_id === sessionId && m.state === "active"),
  );
}

export function skillName(domain: Domain, id: string | undefined): string {
  return domain.skills.find((s: Skill) => s.id === id)?.name ?? "";
}

/** HTTP read against the host's bounded workspace endpoints. */
export async function getJSON<R>(conn: Connection, path: string, q: Record<string, string>): Promise<R> {
  const u = new URL(path, conn.baseURL);
  for (const [k, v] of Object.entries(q)) u.searchParams.set(k, v);
  const res = await fetch(u, { headers: { "X-PenguPool-Token": conn.token } });
  const body = (await res.json()) as R & { error?: string; message?: string };
  if (!res.ok) throw new Error(body.message ?? body.error ?? `HTTP ${res.status}`);
  return body;
}

/** A per-viewer convenience stored in localStorage; never state that matters. */
export function usePersisted<T>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const raw = localStorage.getItem("pengupool." + key);
      return raw === null ? initial : (JSON.parse(raw) as T);
    } catch {
      return initial;
    }
  });
  const set = (next: T): void => {
    setV(next);
    try {
      localStorage.setItem("pengupool." + key, JSON.stringify(next));
    } catch {
      /* private mode: keep it in memory */
    }
  };
  return [v, set];
}

const STATUS: Record<string, { label: string; dot: string; text: string }> = {
  working: { label: "Active", dot: "bg-success animate-pulse", text: "text-success" },
  idle: { label: "Waiting", dot: "bg-warning", text: "text-warning" },
  starting: { label: "Starting", dot: "bg-faint animate-pulse", text: "text-muted-foreground" },
  stale: { label: "Stale", dot: "bg-faint", text: "text-muted-foreground" },
  terminal: { label: "Not an agent", dot: "bg-warning", text: "text-warning" },
  approval: { label: "Needs you", dot: "bg-warning", text: "text-warning" },
  ended: { label: "Ended", dot: "bg-faint", text: "text-faint" },
  exited: { label: "Exited", dot: "bg-faint", text: "text-faint" },
  lost: { label: "Lost", dot: "bg-danger", text: "text-danger" },
  unreachable: { label: "Unreachable", dot: "bg-danger", text: "text-danger" },
};

/** A session waiting longer than this without harness activity reads as Stale (v1's rule). A helper
 * of a live team never does: waiting for its coordinator is its job, not neglect. */
const STALE_MS = 10 * 60 * 1000;

/** The agent runtimes PenguPool runs (mirrors harness.Agents on the host). Any of them can share a
 * team: messages go through PenguPool, which delivers each in the recipient's own way. */
// Claude Code (on its account or on Ollama) is what PenguPool supports first; the others are experimental.
export const AGENTS = [
  { id: "claude", label: "Claude Code", hint: "Anthropic's coding agent", clear: "/clear", experimental: false },
  { id: "muse", label: "Muse Code", hint: "Meta's coding agent", clear: "", experimental: false },
  { id: "omp", label: "OMP", hint: "oh-my-pi agent", clear: "/new", experimental: true },
  { id: "pi", label: "pi", hint: "pi coding agent", clear: "/new", experimental: true },
  { id: "codex", label: "Codex", hint: "OpenAI's coding agent", clear: "/new", experimental: true },
] as const;

/** The picker's choice for Claude Code on an Ollama model, launched with `ollama launch claude`. */
export const CLAUDE_OLLAMA = "claude-ollama";

/** What a picked agent launches: Claude on Ollama is Claude with the model "ollama/<model>" ("ollama/" for the configured one). */
export function launchAgent(choice: string, model: string): { harness: string; model?: string } {
  const m = model.trim();
  if (choice === CLAUDE_OLLAMA) return { harness: "claude", model: `ollama/${m}` };
  return { harness: choice, model: m === "" ? undefined : m };
}

/** The agent picker's options, Claude Code on Ollama right after Claude Code. */
export function agentOptions(agents: readonly { id: string; label: string; experimental: boolean }[], missing: (id: string) => boolean, note: (id: string) => string = () => ""): ReactElement[] {
  const out: ReactElement[] = [];
  for (const a of agents) {
    out.push(
      <option key={a.id} value={a.id} disabled={missing(a.id)}>
        {a.label}
        {a.experimental ? " (experimental)" : ""}
        {missing(a.id) ? " — not installed" : ""}
        {note(a.id)}
      </option>,
    );
    if (a.id === "claude")
      out.push(
        <option key={CLAUDE_OLLAMA} value={CLAUDE_OLLAMA} disabled={missing("claude")}>
          Claude Code (Ollama){missing("claude") ? " — not installed" : ""}
        </option>,
      );
  }
  return out;
}

export function isAgent(s: { harness?: string }): boolean {
  return AGENTS.some((a) => a.id === s.harness);
}

export function sessionStatus(s: { status?: string; state: string; harness?: string; active_at?: string; role?: string }): string {
  if (s.state !== "running") return s.state;
  if (!isAgent(s)) return "terminal";
  const st = s.status ?? "idle";
  if (st === "idle" && s.role !== "helper" && s.active_at !== undefined && s.active_at !== "" && Date.now() - Date.parse(s.active_at) > STALE_MS) return "stale";
  return st;
}

/** How pressing a state is, for a folded group's badge: approval first. */
export const URGENCY: Record<string, number> = { starting: 1, approval: 5, lost: 4, unreachable: 4, working: 3, idle: 2, terminal: 1, stale: 1, exited: 0, ended: 0 };

export function statusStyle(key: string): { label: string; dot: string; text: string } {
  return STATUS[key] ?? STATUS.idle;
}

/** Context-window use by level: green below 30 %, amber below 60 %, red from 60 % up (as in v1). */
export function ctxClass(pct: number): string {
  return pct < 30 ? "text-success" : pct < 60 ? "text-warning" : "text-danger";
}

export function StatusDot({ status, state, label, harness, active_at, role }: { status?: string; state: string; label?: boolean; harness?: string; active_at?: string; role?: string }): ReactElement {
  const s = STATUS[sessionStatus({ status, state, harness: harness ?? "claude", active_at, role })] ?? STATUS.idle;
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-caption", s.text)} title={s.label}>
      <span className={cn("size-2 shrink-0 rounded-full", s.dot)} />
      {label === true ? s.label : <span className="sr-only">{s.label}</span>}
    </span>
  );
}

export function ago(iso: string | undefined): string {
  if (iso === undefined || iso === "") return "";
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function harnessLabel(h: string | undefined): string {
  return AGENTS.find((a) => a.id === h)?.label ?? { shell: "Shell", custom: "Command" }[h ?? "shell"] ?? h ?? "Shell";
}
