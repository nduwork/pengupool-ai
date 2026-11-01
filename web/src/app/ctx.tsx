import { createContext, useContext, useState, type ReactElement, type ReactNode } from "react";

import type { Connection } from "../client/connection";
import type { SessionView } from "../protocol/control";
import type { Domain, Skill, Team } from "../protocol/domain";
import type { OpenGate } from "../views/inbox";
import { cn } from "../lib/utils";

export type Nav = "workspace" | "skills" | "settings";

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
  /** The go-aheads triage picked for the owner to check (compass.spot_checks), read every 30 s and after each action. */
  spots: SpotCheck[];
  select: (id: string | null) => void;
  nav: Nav;
  setNav: (n: Nav) => void;
  /** Runs a pool request and reports a failure as a toast; resolves undefined on failure. */
  call: <R = unknown>(type: string, params?: unknown) => Promise<R | undefined>;
  openTab: (t: EditorTab) => void;
  showDialog: (d: ReactNode) => void;
  /** Welcome: whether it shows when the Workspace is empty (per viewer), and opening it explicitly. */
  welcome: { show: boolean; setShow: (v: boolean) => void; open: () => void };
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
  // Waiting on the owner (a permission prompt or a check-in): red, with a "?" besides the colour.
  approval: { label: "? Need input", dot: "bg-danger", text: "text-danger" },
  ended: { label: "Ended", dot: "bg-faint", text: "text-faint" },
  exited: { label: "Exited", dot: "bg-faint", text: "text-faint" },
  lost: { label: "Lost", dot: "bg-danger", text: "text-danger" },
  unreachable: { label: "Unreachable", dot: "bg-danger", text: "text-danger" },
};

/** A session waiting longer than this without harness activity reads as Stale (v1's rule). A helper
 * of a live team never does: waiting for its coordinator is its job, not neglect. */
const STALE_MS = 10 * 60 * 1000;

/** The agent runtimes PenguPool runs (mirrors harness.Agents on the host). Any of them can share a
 * team: messages go through PenguPool, which delivers each in the recipient's own way. Hidden ones are
 * not offered; their old sessions still run and resume. */
export const AGENTS = [
  { id: "claude", label: "Claude Code", hint: "Anthropic's coding agent", clear: "/clear", hidden: false },
  { id: "muse", label: "Muse Code", hint: "Meta's coding agent", clear: "", hidden: false },
  { id: "codex", label: "Codex", hint: "OpenAI's coding agent", clear: "/new", hidden: false },
  { id: "pi", label: "pi", hint: "pi coding agent", clear: "/new", hidden: false },
  { id: "omp", label: "OMP", hint: "oh-my-pi agent", clear: "/new", hidden: false },
  { id: "copilot", label: "GitHub Copilot", hint: "GitHub Copilot CLI", clear: "/clear", hidden: true },
  { id: "gemini", label: "Gemini CLI", hint: "Google's agent (Google login)", clear: "/clear", hidden: true },
] as const;

/** The picker's choice for Claude Code (Ollama): a full Claude session launched through `ollama launch
 * claude` on the model Ollama itself would preselect. */
export const CLAUDE_OLLAMA = "claude-ollama";

/** The installed-agents check (harness.probe): a version, or why it is not there. "ollama" is the
 * ollama CLI (its path as the version), with the model it would start, or why none can start. */
export type Found = Record<string, { version: string; error?: string; model?: string }>;

/** When the install check itself fails, every agent is offered, as if installed: a failed check never blocks a launch. */
export const checkFailed = (): Found => Object.fromEntries([...AGENTS.map((a) => a.id), "ollama"].map((id) => [id, { version: "" }]));

type Pick = { value: string; label: string; agent: string; disabled?: string };

const installed = (found: Found, id: string): boolean => found[id] !== undefined && found[id].error === undefined;

/** What a picker offers: installed agents only (once the check answered), and with ollama Claude Code
 * (Ollama) right after Claude Code; when Ollama has no model to start, it is shown disabled with why. */
export function pickable(found: Found | null, agents: readonly { id: string; label: string; hidden: boolean }[] = AGENTS, ollama = false): Pick[] {
  const out: Pick[] = [];
  if (found === null) return out;
  for (const a of agents) {
    if (a.hidden || !installed(found, a.id)) continue;
    out.push({ value: a.id, label: a.label, agent: a.id });
    const o = found.ollama;
    if (ollama && a.id === "claude" && o !== undefined && (o.error === undefined || o.version !== ""))
      out.push({ value: CLAUDE_OLLAMA, label: "Claude Code (Ollama)", agent: "claude", disabled: o.error });
  }
  return out;
}

/** What New session starts on for a child: its parent's agent, through Ollama when the parent was. */
export function parentPick(parent: { harness?: string; via?: string } | undefined): string | undefined {
  if (parent?.harness === "claude" && parent.via === "ollama") return CLAUDE_OLLAMA;
  return AGENTS.some((a) => a.id === parent?.harness) ? parent?.harness : undefined;
}

/** What is shown is what is sent: the wanted pick while it can start, else Claude Code (Ollama) when it
 * is the default (moved from "every Claude Code session on Ollama"), else the first that can start. */
export function choosePick(options: readonly Pick[], want: string, ollamaDefault: boolean): string {
  const ok = (v: string): boolean => options.some((o) => o.value === v && o.disabled === undefined);
  if (ok(want)) return want;
  if (ollamaDefault && ok(CLAUDE_OLLAMA)) return CLAUDE_OLLAMA;
  return options.find((o) => o.disabled === undefined)?.value ?? "";
}

/** New session may start only once both the install check and the settings have answered: before
 * that the shown pick may still change (to Claude Code (Ollama), say). */
export const pickReady = (found: Found | null, settingsLoaded: boolean): boolean => found !== null && settingsLoaded;

/** What a picked option launches: an agent on its own default model, or Claude through Ollama on Ollama's model. */
export function launchAgent(choice: string): { harness: string; via?: string } {
  return choice === CLAUDE_OLLAMA ? { harness: "claude", via: "ollama" } : { harness: choice };
}

/** The picker's options, from pickable(). */
export function agentOptions(options: readonly Pick[], note: (value: string) => string = () => ""): ReactElement[] {
  return options.map((o) => (
    <option key={o.value} value={o.value} disabled={o.disabled !== undefined}>
      {o.label}
      {o.disabled !== undefined ? " — not ready" : ""}
      {note(o.value)}
    </option>
  ));
}

/** A raw terminal tab: the owner's login shell, on no team and with no agent (shell.open). */
export function isShell(s: { harness?: string }): boolean {
  return s.harness === "shell";
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

/** What a session's card and dot show: "approval" also when it waits on the owner's answer to a
 * check-in, the condition the Needs-you bar uses (inbox.ts sessionNeeds). */
export function shownStatus(s: { id: string; status?: string; state: string; harness?: string; active_at?: string; role?: string }, gates: readonly { session_id: string; kind: string }[]): string {
  const st = sessionStatus(s);
  if (s.state === "running" && st !== "working" && st !== "lost" && st !== "unreachable" && gates.some((g) => g.session_id === s.id && g.kind === "checkin")) return "approval";
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

export function StatusDot({ id, status, state, label, harness, active_at, role }: { id: string; status?: string; state: string; label?: boolean; harness?: string; active_at?: string; role?: string }): ReactElement {
  const app = useApp();
  const s = STATUS[shownStatus({ id, status, state, harness: harness ?? "claude", active_at, role }, app.gates)] ?? STATUS.idle;
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

/** The "no hooks" note's long text, and what the owner can do about it. */
export const HOOKS_LONG = "PenguPool's hooks aren't reaching this agent: no Compass or team context.";
export function hooksAction(h: string | undefined): string {
  const check = h === "muse" ? "check that the PenguPool plugin is installed and approved" : "check its PenguPool install (Settings ▸ Agents)";
  return `Restart the session. If it stays, update ${harnessLabel(h)} or ${check}.`;
}

export function harnessLabel(h: string | undefined): string {
  return AGENTS.find((a) => a.id === h)?.label ?? { shell: "Shell", custom: "Command" }[h ?? "shell"] ?? h ?? "Shell";
}

/** A go-ahead triage gave that the owner is asked to check: right or wrong. */
export interface SpotCheck {
  id: number;
  agent: string;
  question: string;
  created_at?: string;
  team_id?: string;
}
