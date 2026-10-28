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

/** One thing compass.review asks the owner to tidy: a broad line (remove it or keep it), lines to merge into patterns, or a line pulled both ways (restate or remove it). */
export interface ReviewItem {
  kind: "broad" | "merge" | "conflicted";
  way?: "allow" | "deny";
  why: string;
  lines: { uid: string; body: string }[];
  patterns?: string[];
}

/** The heading of a review item. */
export function reviewTitle(it: ReviewItem): string {
  return { broad: "Broad", merge: "Merge", conflicted: "Pulled both ways" }[it.kind] + ": " + it.why;
}
