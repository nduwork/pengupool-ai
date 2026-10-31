// The Compass panel's data: a line as compass.list returns it.

export interface Line {
  uid: string;
  kind: string;
  tag?: string;
  body: string;
  condition?: string;
  source?: string;
  created_at: string;
  mu: number;
  n: number;
  sigma: number;
  state: "confident" | "learning" | "fading";
  effects: number;
}

const GROUPS: Array<{ state: Line["state"]; title: string }> = [
  { state: "confident", title: "Guiding" },
  { state: "learning", title: "Learning" },
  { state: "fading", title: "Fading" },
];

/** The lines by state, each strongest first. Empty groups are left out. */
export function groupLines(lines: Line[]): Array<{ title: string; lines: Line[] }> {
  return GROUPS.map((g) => ({ title: g.title, lines: lines.filter((l) => l.state === g.state).sort((a, b) => b.mu - a.mu) }))
    .filter((g) => g.lines.length > 0);
}

/** One line in a review item; a conflict's lines carry updated_at, sent back so a stale view is refused. */
export interface ReviewLine {
  uid: string;
  body: string;
  condition?: string;
  updated_at?: string;
}

/** One thing compass.review asks the owner to tidy: a broad line (remove it or keep it), lines that contradict (keep one), or lines to merge into patterns. */
export interface ReviewItem {
  kind: "broad" | "conflict" | "merge";
  way?: "allow" | "deny";
  why: string;
  lines: ReviewLine[];
  patterns?: string[];
}
