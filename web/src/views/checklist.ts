import type { Nav } from "../app/ctx";

/** What the owner has set up, as far as the host says. */
export interface Setup {
  /** The first ready agent and its version ("Claude Code 2.1.4"); "" when none is installed. */
  agent: string;
  /** The model used for triage (it passed its check before Use); "" when none is. */
  decision: string;
  /** The Agent permissions choice: "" not decided yet, "allowed" or "off". */
  permissions: string;
}

export interface Step {
  id: string;
  label: string;
  done: boolean;
  /** Done: the current value. Not done: why it matters. */
  detail: string;
  /** The action's label when not done. */
  action: string;
  /** Where it is done: a page, and a Settings section. */
  nav: Nav;
  section?: string;
}

export const stepsOf = (s: Setup): Step[] => [
  { id: "agent", label: "Agent", done: s.agent !== "", detail: s.agent !== "" ? s.agent : "No agent installed", action: "Set up", nav: "settings", section: "agents" },
  { id: "decision", label: "Decision model", done: s.decision !== "", detail: s.decision !== "" ? s.decision : "Required for memory and triage", action: "Set up", nav: "settings", section: "models" },
  {
    id: "permissions",
    label: "Agent permissions",
    done: s.permissions !== "",
    detail: s.permissions === "allowed" ? "Allowed" : s.permissions === "off" ? "Asking each time" : "Not decided",
    action: "Decide",
    nav: "settings",
    section: "agents",
  },
];
