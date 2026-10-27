// Turns a session's plain-text run log ("HH:MM:SS kind: text" lines, written by internal/pool)
// into a document: one section per run of the agent, with messages, team events, approvals,
// memory and task-log notes as entries, and runs of status changes folded into one line.

export type EntryKind = "message" | "team" | "approval" | "memory" | "owner" | "status" | "session" | "note";

export interface Entry {
  time: string;
  kind: EntryKind;
  text: string;
  /** Messages only. */
  from?: string;
  to?: string;
}

export interface Run {
  /** The first line of the run ("started omp in …"), empty for lines logged before any start. */
  title: string;
  time: string;
  entries: Entry[];
}

const LINE = /^(\d\d:\d\d:\d\d) (.*)$/;
const MESSAGE = /^(.+?) → (.+?): ([\s\S]*)$/;
const KINDS: Record<string, EntryKind> = { team: "team", approval: "approval", memory: "memory", log: "memory", owner: "owner", status: "status", session: "session", message: "message" };

export function parseRunLog(content: string): Run[] {
  const runs: Run[] = [];
  let run: Run | undefined;
  const push = (e: Entry): void => {
    if (run === undefined) {
      run = { title: "", time: e.time, entries: [] };
      runs.push(run);
    }
    const last = run.entries[run.entries.length - 1];
    // Fold consecutive status changes into one "busy → idle" entry.
    if (e.kind === "status" && last?.kind === "status") {
      last.text = `${last.text} → ${e.text}`;
      last.time = e.time;
      return;
    }
    run.entries.push(e);
  };
  for (const raw of content.split("\n")) {
    if (raw.trim() === "") continue;
    const m = LINE.exec(raw);
    const time = m?.[1] ?? "";
    const body = (m?.[2] ?? raw).replaceAll(" ⏎ ", "\n");
    const msg = MESSAGE.exec(body);
    const colon = body.indexOf(": ");
    const kind = colon > 0 ? KINDS[body.slice(0, colon)] : undefined;
    if (kind === "session" && /^session: started /.test(body)) {
      run = { title: body.slice("session: ".length), time, entries: [] };
      runs.push(run);
    } else if (kind !== undefined) {
      push({ time, kind, text: body.slice(colon + 2) });
    } else if (msg !== null) {
      push({ time, kind: "message", from: msg[1], to: msg[2], text: msg[3] });
    } else {
      push({ time, kind: "note", text: body });
    }
  }
  return runs;
}
