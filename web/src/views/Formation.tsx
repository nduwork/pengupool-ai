import { useEffect, useState, type ReactElement } from "react";

import { AGENTS, CLAUDE_OLLAMA, agentOptions, launchAgent, skillName, useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { cn } from "../lib/utils";
import { Input, Label, Select, Textarea, type Role } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { Domain, FormationChange, Member, Team } from "../protocol/domain";

// Formation: every change to who is on a team is approved by the owner. A change made here is
// approved in the same step (team.formation); a member's request waits in Approvals. Who leads is not
// a change: the LEAD is the root of the team's tree.

/** The session id of the team's LEAD: the root of its tree, the main worker, else the first active
 * member with no active parent (mirrors the host's leadOf). */
export function leadOf(t: Team): string {
  const active = t.members.filter((m) => m.state === "active");
  return (active.find((m) => m.role === "main") ?? active.find((m) => treeParent(t, m.session_id) === ""))?.session_id ?? "";
}

/** A member's nearest active ancestor, past members that left; "" at the top (mirrors the host's treeParent). */
function treeParent(t: Team, id: string): string {
  const parent = (x: string): string => t.members.find((m) => m.session_id === x)?.parent ?? "";
  const seen = new Set([id]);
  for (let up = parent(id); up !== "" && !seen.has(up); up = parent(up)) {
    seen.add(up);
    if (t.members.some((m) => m.session_id === up && m.state === "active")) return up;
  }
  return "";
}

/** A member's label, derived, never stored (mirrors the host's teamLabel): LEAD for the root of the
 * tree, HELPER for an agent from the helper pool, SME for a member the owner added with a repository
 * behind it (its worktree, or a session working in a known repository); undefined for any other member. */
export function teamLabel(t: Team, m: Member, cwd?: string, repos: { path: string }[] = []): Role | undefined {
  if (m.session_id === leadOf(t)) return "lead";
  if (m.skill_id !== undefined && m.skill_id !== "") return "helper";
  if ((m.repo_id ?? "") !== "" || (m.worktree ?? "") !== "") return "sme";
  if (cwd !== undefined && cwd !== "" && repos.some((r) => r.path !== "" && (cwd === r.path || cwd.startsWith(r.path + "/")))) return "sme";
  return undefined;
}

/** The role of a session on its active team; undefined for a session on no team. */
export function roleOf(domain: Domain, sessions: readonly { id: string; cwd?: string }[], sessionId: string): Role | undefined {
  const cwd = sessions.find((x) => x.id === sessionId)?.cwd;
  for (const t of domain.teams) {
    if (t.state !== "active") continue;
    const m = t.members.find((x) => x.session_id === sessionId && x.state === "active");
    if (m !== undefined) return teamLabel(t, m, cwd, domain.repos);
  }
  return undefined;
}

export function describeChange(domain: Domain, t: Team | undefined, c: FormationChange): string {
  const member = t?.members.find((m) => m.session_id === c.session_id)?.name ?? c.session_id ?? "";
  switch (c.op) {
    case "add":
      return c.skill_id !== undefined && c.skill_id !== ""
        ? `Add ${skillName(domain, c.skill_id)} as a helper agent`
        : `Add an SME for ${domain.repos.find((r) => r.id === c.repo_id)?.name ?? "a repository"}`;
    case "remove":
      return `Remove ${member}`;
    default: // a request from an older record to choose who leads; it changes nothing
      return `${String(c.op)} ${member}`;
  }
}

/** The inline approval of one formation change: the owner's own, or a member's request (approvalId). */
export function FormationSheet({ team, change, onDone, approvalId }: { team: Team; change: FormationChange; onDone?: () => void; approvalId?: string }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const [reason, setReason] = useState(change.reason ?? "");
  const [busy, setBusy] = useState(false);
  // Adding a member starts an agent: the owner picks which one and its model, as in New session.
  const adding = change.op === "add";
  const skill = app.domain.skills.find((s) => s.id === change.skill_id);
  const allowed = AGENTS.filter((a) => (skill?.requires ?? []).length === 0 || (skill?.requires ?? []).includes(a.id));
  const [found, setFound] = useState<Record<string, { version: string; error?: string }> | null>(null);
  useEffect(() => {
    if (adding) void app.conn.request<Record<string, { version: string; error?: string }>>("harness.probe").then(setFound, () => setFound({}));
  }, [adding]);
  const missing = (id: string): boolean => (id === CLAUDE_OLLAMA ? missing("claude") : found !== null && found[id] !== undefined && found[id].error !== undefined);
  const [want, setHarness] = useState(change.harness ?? skill?.harness ?? (team.harness || "claude"));
  // What is shown is what is sent: a wanted agent the helper may not run on, or that is not
  // installed, gives way to the first one that is.
  const usable = allowed.filter((a) => !missing(a.id));
  const harness = usable.some((a) => a.id === launchAgent(want, "").harness) ? want : (usable[0]?.id ?? want);
  const agent = launchAgent(harness, "").harness;
  const [model, setModel] = useState(change.model ?? "");
  // Which agents run on a local Ollama model (Settings → Models & agents → Agents on this machine), as New session shows it.
  const [models, setModels] = useState<{
    ollama_model?: string;
    ollama?: string[] | null;
  } | null>(null);
  useEffect(() => {
    if (adding)
      void app.conn
        .request<{
          agent_models?: { ollama_model?: string; ollama?: string[] | null };
        }>("settings.get")
        .then(
          (r) => setModels(r.agent_models ?? null),
          () => undefined,
        );
  }, [adding]);
  const onOllama =
    harness === CLAUDE_OLLAMA ? model.trim() || models?.ollama_model || "name a model below" : (models?.ollama ?? []).includes(harness) ? model.trim() || models?.ollama_model || "" : "";
  const needsModel = harness === CLAUDE_OLLAMA && model.trim() === "" && (models?.ollama_model ?? "") === "";
  const picked = adding ? launchAgent(harness, model) : {};
  const submit = async (): Promise<void> => {
    if (busy || (adding && (missing(harness) || usable.length === 0 || needsModel))) return;
    setBusy(true);
    const r =
      approvalId !== undefined
        ? await app.call("approval.decide", { id: approvalId, decision: "approve", ...picked })
        : await app.call("team.formation", {
            team_id: team.id,
            change: { ...change, ...picked, reason },
          });
    setBusy(false);
    if (r !== undefined) {
      close();
      onDone?.();
    }
  };
  return (
    <Sheet
      title={adding ? (approvalId !== undefined ? "Approve adding a member" : "Add member") : "Approve a formation change"}
      description={`Team ${team.name}: ${describeChange(app.domain, team, change)}`}
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="default" disabled={busy || (adding && (missing(harness) || usable.length === 0 || needsModel))} onClick={() => void submit()}>
            {adding ? (busy ? "Adding…" : approvalId !== undefined ? "Approve and add" : "Add member") : busy ? "Applying…" : "Approve change"}
          </Button>
        </>
      }
    >
      <form
        className="grid grid-cols-1 gap-4 text-label"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {adding ? null : (
          <p className="text-muted-foreground">
            A member that leaves keeps its own experience.
          </p>
        )}
        {adding ? (
          <>
            <Label
              hint={
                found === null
                  ? "Checking which agents are installed…"
                  : usable.length === 0
                    ? `None of the agents ${skill?.name ?? "it"} runs on is installed here.`
                    : allowed.length === 1
                      ? `Only ${allowed[0].label} can run ${skill?.name ?? "this helper"}.`
                      : (found[agent]?.error ?? `${AGENTS.find((a) => a.id === agent)?.hint ?? ""} · ${found[agent]?.version ?? ""}${onOllama !== "" ? ` · runs on Ollama: ${onOllama}` : ""}`)
              }
            >
              Agent
              <Select value={harness} onChange={(e) => setHarness(e.target.value)} aria-label="Agent runtime" autoFocus>
                {agentOptions(allowed, missing, (id) => (skill?.harness === id ? " (helper's default)" : ""))}
              </Select>
            </Label>
            <Label>
              Model <span className="font-normal text-faint">{needsModel ? "required" : "optional"}</span>
              <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder={harness === CLAUDE_OLLAMA ? (models?.ollama_model ?? "an Ollama model, e.g. qwen3-coder") : "harness default"} />
            </Label>
          </>
        ) : null}
        {approvalId !== undefined ? (
          change.assignment || change.reason ? (
            <p className="text-muted-foreground">Asked for: {change.assignment || change.reason}</p>
          ) : null
        ) : (
          <Label hint={adding ? "Kept with the change, in the team's record." : "Optional; kept with the change."}>
            Reason
            {adding ? <span className="font-normal text-faint"> optional</span> : null}
            <Input value={reason} onChange={(e) => setReason(e.target.value)} />
          </Label>
        )}
        <button type="submit" className="hidden" />
      </form>
    </Sheet>
  );
}

export function AddMemberSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const taken = new Set(team.members.filter((m) => m.state === "active").map((m) => m.skill_id));
  const skills = app.domain.skills.filter((s) => !taken.has(s.id));
  const [kind, setKind] = useState<"freelancer" | "sme">(skills.length > 0 ? "freelancer" : "sme");
  const [skill, setSkill] = useState(skills[0]?.id ?? "");
  const [repo, setRepo] = useState(app.domain.repos[0]?.id ?? "");
  const [assignment, setAssignment] = useState("");
  const change: FormationChange = kind === "freelancer" ? { op: "add", skill_id: skill, assignment } : { op: "add", repo_id: repo, assignment };
  return (
    <Sheet
      title={`Add a member to ${team.name}`}
      description="A helper agent comes from Helper agents; an SME is an agent for one more repository. Each works in its own worktree."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="default" disabled={kind === "freelancer" ? skill === "" : repo === ""} onClick={() => app.showDialog(<FormationSheet team={team} change={change} />)}>
            Continue
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-1 gap-4">
        <div className="flex gap-1 rounded-lg bg-muted p-1 text-label">
          {(["freelancer", "sme"] as const).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setKind(k)}
              aria-pressed={kind === k}
              className={cn("flex h-7 flex-1 items-center justify-center rounded-md", kind === k ? "bg-panel font-medium shadow-sm" : "text-muted-foreground")}
            >
              {k === "freelancer" ? "Helper agent" : "Repository SME"}
            </button>
          ))}
        </div>
        {kind === "freelancer" ? (
          <Label>
            Helper
            <Select value={skill} onChange={(e) => setSkill(e.target.value)}>
              {skills.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          </Label>
        ) : (
          <Label>
            Repository
            <Select value={repo} onChange={(e) => setRepo(e.target.value)}>
              {app.domain.repos.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </Select>
          </Label>
        )}
        <Label hint="Optional; a default is written from the team's task.">
          Assignment
          <Textarea value={assignment} onChange={(e) => setAssignment(e.target.value)} />
        </Label>
      </div>
    </Sheet>
  );
}

export function memberBadge(domain: Domain, m: Member): string {
  if (m.role === "main") return "Main";
  if (m.role === "sme") return `SME · ${domain.repos.find((r) => r.id === m.repo_id)?.name ?? "repository"}`;
  return skillName(domain, m.skill_id) || "Helper";
}
