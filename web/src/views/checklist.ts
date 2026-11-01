import type { Nav } from "../app/ctx";

/** What the owner has set up, as far as the app can tell. */
export interface Setup {
  agent: boolean; // Claude Code is installed on this machine
  teams: number;
  decision: boolean; // a decision model is active
  permissions: boolean; // the owner has decided Agent permissions, either way
}

export interface Step {
  id: string;
  label: string;
  done: boolean;
  /** Where it is done: a page, and a Settings section. */
  nav: Nav;
  section?: string;
}

export const stepsOf = (s: Setup): Step[] => [
  { id: "agent", label: "Set up Claude Code", done: s.agent, nav: "settings", section: "agents" },
  { id: "team", label: "Create a team", done: s.teams > 0, nav: "workspace" },
  { id: "decision", label: "Set a decision model", done: s.decision, nav: "settings", section: "models" },
  { id: "permissions", label: "Decide agent permissions", done: s.permissions, nav: "settings", section: "agents" },
];
