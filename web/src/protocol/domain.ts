// Pool state as internal/protocol/domain.go sends it in state.snapshot / state.delta.

export interface Repo {
  id: string;
  name: string;
  path: string;
  kind: string;
  url?: string;
  git: boolean;
  node_id?: string;
  created_at: string;
}

export interface SkillFile {
  name: string;
  description?: string;
  license?: string;
  compatibility?: string;
  allowed_tools?: string;
  metadata?: Record<string, string>;
  digest: string;
  problem?: string;
}

export interface Skill {
  id: string;
  name: string;
  path: string;
  method?: string;
  description?: string;
  capabilities?: string[];
  /** Preferred agent. */
  harness?: string;
  /** Agents it can only run on; empty: any. */
  requires?: string[];
  source: string;
  commit?: string;
  files?: SkillFile[];
  profiles?: string[];
  /** Listed in the map's Helper agent pool. */
  on_map?: boolean;
  key?: string;
  /** Helper group on the Helper agents page; empty: ungrouped. */
  group?: string;
  created_at: string;
}

export interface Task {
  id: string;
  title: string;
  objective: string;
  repo_id: string;
  harness: string;
  capabilities?: string[];
  state: string;
  team_id?: string;
  failure?: string;
  created_at: string;
  updated_at: string;
}

export interface ProposedMember {
  skill_id?: string;
  name: string;
  role: string;
  assignment: string;
  coordinator?: boolean;
  model?: string;
  /** Empty: the team's default agent. */
  harness?: string;
}

export interface Proposal {
  id: string;
  task_id: string;
  version: number;
  members: ProposedMember[];
  provider: string;
  model?: string;
  confidence: number;
  fallback?: boolean;
  warnings?: string[];
  gaps?: string[];
  created_at: string;
}

export interface Member {
  session_id: string;
  name: string;
  role: string;
  skill_id?: string;
  /** An SME's repository (empty: the team's). */
  repo_id?: string;
  assignment?: string;
  worktree?: string;
  branch?: string;
  base_commit?: string;
  coordinator?: boolean;
  state: string;
  parent?: string;
}

export interface Team {
  id: string;
  name: string;
  task_id?: string;
  repo_id?: string;
  /** Further repositories the team works in (its SMEs). */
  repos?: string[];
  harness: string;
  /** The team's job; it evolves, edited in place (a direction change). */
  goal?: string;
  /** The member every message reaches and who composes the results: a hub, not a boss. */
  anchor_id?: string;
  /** What each member session spent while on the team, in USD, as its harness reports it. */
  cost_by?: Record<string, number>;
  /** The owner's budget for the team's job; unset: the app shows an estimate. */
  budget_usd?: number;
  members: Member[];
  state: string;
  ios_access: boolean;
  password_set: boolean;
  created_at: string;
}

export interface FormationChange {
  op: "add" | "remove" | "anchor";
  skill_id?: string;
  repo_id?: string;
  session_id?: string;
  assignment?: string;
  reason?: string;
  /** The member who asked; empty: the owner. */
  requested_by?: string;
  /** add: the agent to run it on, and its model; empty: the defaults. */
  harness?: string;
  model?: string;
}

export interface Approval {
  id: string;
  /** launch | formation | permission */
  kind: string;
  team_id?: string;
  change?: FormationChange;
  task_id?: string;
  version?: number;
  session_id?: string;
  tool?: string;
  detail?: string;
  state: string;
  created_at: string;
  expires_at?: string;
}

export interface TeamMessage {
  id: string;
  team_id: string;
  from: string;
  to: string;
  text: string;
  state: string;
  reason?: string;
  at: string;
}

export interface NodeView {
  id: string;
  name: string;
  kind: string;
  address?: string;
  status: string;
  reason?: string;
  hub?: string;
  auth?: "key" | "password";
  identity_file?: string;
  saved_password?: boolean;
  version?: string;
  outdated?: boolean;
}

export interface Domain {
  repos: Repo[];
  skills: Skill[];
  tasks: Task[];
  proposals: Proposal[];
  teams: Team[];
  approvals: Approval[];
  messages: TeamMessage[];
  nodes: NodeView[];
  /** A newer PenguPool release, when the host's update check found one. */
  update?: Update;
}

/** The host's update check (update.check): the newest release and whether it is newer. */
export interface Update {
  current: string;
  latest?: string;
  tag?: string;
  newer: boolean;
  url?: string;
  /** Release notes, plain text, at most 2 KB. */
  notes?: string;
  asset?: string;
  error?: string;
}

export const EMPTY_DOMAIN: Domain = {
  repos: [],
  skills: [],
  tasks: [],
  proposals: [],
  teams: [],
  approvals: [],
  messages: [],
  nodes: [],
};

/** One entry of a helper's journal (host `experience.list`; `body` only from `experience.get`). */
export interface JournalEntry {
  id: number;
  helper: string;
  at: string;
  title: string;
  lesson?: string;
  body?: string;
}

