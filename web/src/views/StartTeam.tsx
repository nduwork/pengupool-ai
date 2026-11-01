import { Check, FolderOpen, Loader2, Search, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { AGENTS, pickable, useApp, usePersisted, type Found } from "../app/ctx";
import { localDesktop } from "../app/desktop";
import { FolderBrowser } from "../components/FolderBrowser";
import { Button } from "../components/ui/button";
import { Input, Label, Select } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { Skill } from "../protocol/domain";

/** The lead of a team started here: the Specialist hub's team-hub agent. */
export const LEAD = "team-lead-agent";

/** Commonly useful specialists, in this order; only the installed ones show. Every other one is a search away. */
export const SUGGESTED = ["research-agent", "pr-review-agent", "dev-workflow-agent", "security-agent", "api-design-agent", "git-ci-agent", "data-analysis-agent"];

/** The installed specialists Start with a specialist offers: the lead excluded, Suggested first in its order. */
export function suggested(skills: Skill[]): Skill[] {
  return SUGGESTED.map((n) => skills.find((s) => s.name === n)).filter((s): s is Skill => s !== undefined);
}

/** Specialists matching a search by name, description or capability. */
export function matching(skills: Skill[], q: string): Skill[] {
  const w = q.trim().toLowerCase();
  if (w === "") return skills;
  return skills.filter((s) => [s.name, s.description ?? "", s.method ?? "", ...(s.capabilities ?? [])].some((x) => x.toLowerCase().includes(w)));
}

interface StartResult {
  team_id: string;
  name: string;
  lead: string;
  members: { skill_id: string; name: string; ok: boolean; session_id?: string; error?: string }[];
}

/** Start with a specialist: a new team led by team-lead-agent, with the specialists the owner ticks as members. */
export function StartTeamSheet({ onClose, onStarted }: { onClose: () => void; onStarted?: () => void }): ReactElement {
  const app = useApp();
  const lead = app.domain.skills.find((s) => s.name === LEAD);
  const all = useMemo(() => app.domain.skills.filter((s) => s.name !== LEAD).sort((a, b) => a.name.localeCompare(b.name)), [app.domain.skills]);
  const [q, setQ] = useState("");
  const [showAll, setShowAll] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [savedPath] = usePersisted("new.path", app.domain.repos[0]?.path ?? "");
  const [folder, setFolder] = useState(savedPath);
  const [browsing, setBrowsing] = useState(false);
  const [found, setFound] = useState<Found | null>(null);
  const [agent, setAgent] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<StartResult | null>(null);
  useEffect(() => {
    void app.conn.request<Found>("harness.probe").then(setFound, () => setFound({}));
  }, []);
  const options = pickable(found, AGENTS);
  const pick = agent !== "" ? agent : (options[0]?.value ?? "");
  const shown = q.trim() !== "" ? matching(all, q) : showAll ? all : suggested(all);
  const needFolder = picked.length > 0; // every specialist but the lead works in a repository
  const names = picked.map((id) => all.find((s) => s.id === id)?.name ?? id);
  const toggle = (id: string): void => setPicked(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id]);
  const plural = new Intl.PluralRules("en").select(picked.length) === "one" ? "specialist" : "specialists";
  const canStart = lead !== undefined && pick !== "" && !busy && (!needFolder || folder.trim() !== "");

  const start = async (): Promise<void> => {
    if (!canStart) return;
    setBusy(true);
    const r = await app.call<StartResult>("team.start", { skill_ids: picked, folder: needFolder ? folder.trim() : "", harness: pick });
    setBusy(false);
    if (r === undefined) return; // app.call showed the host's reason; nothing was created
    if (r.members.every((m) => m.ok)) {
      app.select(r.lead);
      onClose();
      onStarted?.();
      toast.success("Team started");
      return;
    }
    setResult(r);
  };

  const retry = async (skillId: string): Promise<void> => {
    if (result === null) return;
    const m = await app.call<{ session_id: string }>("team.formation", { team_id: result.team_id, change: { op: "add", skill_id: skillId, harness: pick } });
    if (m !== undefined) setResult({ ...result, members: result.members.map((x) => (x.skill_id === skillId ? { ...x, ok: true, error: undefined, session_id: m.session_id } : x)) });
  };

  if (result !== null) {
    const ok = result.members.filter((m) => m.ok).length;
    return (
      <Sheet
        title="Start with a specialist"
        description={`Team ${result.name} started with ${ok} of ${result.members.length} specialists.`}
        onClose={onClose}
        footer={
          <Button
            variant="default"
            onClick={() => {
              app.select(result.lead);
              onClose();
              onStarted?.();
            }}
          >
            Go to the team
          </Button>
        }
      >
        <div className="grid gap-2 text-label" role="status">
          {result.members.map((m) => (
            <div key={m.skill_id} className="flex items-start gap-2">
              {m.ok ? <Check className="mt-0.5 size-3.5 shrink-0 text-success" /> : <X className="mt-0.5 size-3.5 shrink-0 text-danger" />}
              <span className="min-w-0 flex-1">
                <span className="font-medium">{m.name}</span> {m.ok ? <span className="text-muted-foreground">Started</span> : <span className="text-danger [overflow-wrap:anywhere]">{m.error}</span>}
              </span>
              {m.ok ? null : (
                <Button size="sm" variant="outline" onClick={() => void retry(m.skill_id)}>
                  Retry
                </Button>
              )}
            </div>
          ))}
          <p className="text-caption text-muted-foreground">The team runs with the members that started. Retry, or add one later from the map.</p>
        </div>
      </Sheet>
    );
  }

  if (lead === undefined) {
    return (
      <Sheet
        title="Start with a specialist"
        onClose={onClose}
        footer={
          <Button
            variant="default"
            onClick={() => {
              onClose();
              app.setNav("skills");
            }}
          >
            Open Specialists
          </Button>
        }
      >
        <p className="rounded-md bg-warning/10 p-3 text-caption text-warning">{LEAD} isn&apos;t installed, so a team can&apos;t be led. Install it from Specialists, then start again.</p>
      </Sheet>
    );
  }

  return (
    <Sheet
      title="Start with a specialist"
      description={`Starts a new team. ${LEAD} leads it; the specialists you pick join as members.`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="default" disabled={!canStart} onClick={() => void start()}>
            {busy ? (
              <>
                <Loader2 className="animate-spin" /> Starting…
              </>
            ) : picked.length === 0 ? (
              "Start team (lead only)"
            ) : (
              `Start team with ${picked.length} ${plural}`
            )}
          </Button>
        </>
      }
    >
      <div
        className="grid gap-4 text-label"
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void start();
        }}
      >
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-faint" />
          <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={`Search all ${all.length} specialists`} className="pl-7" aria-label="Search specialists" />
        </div>
        <div>
          <p className="mb-1 text-caption font-medium text-muted-foreground">
            {q.trim() !== "" ? `${shown.length} ${shown.length === 1 ? "specialist matches" : "specialists match"}` : showAll ? "All specialists" : "Suggested"}
          </p>
          {all.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              No other specialists are installed. Install them from{" "}
              <button
                type="button"
                className="underline"
                onClick={() => {
                  onClose();
                  app.setNav("skills");
                }}
              >
                Specialists
              </button>
              , or start with the lead only.
            </p>
          ) : shown.length === 0 ? (
            <p className="text-caption text-muted-foreground">No specialist matches “{q.trim()}”.</p>
          ) : (
            <div className="grid">
              {shown.map((s) => (
                <label key={s.id} className="flex items-start gap-2 rounded-md px-2 py-1.5 hover:bg-hover">
                  <input type="checkbox" className="mt-0.5" checked={picked.includes(s.id)} onChange={() => toggle(s.id)} />
                  <span className="min-w-0 flex-1">
                    <span className="block font-medium">{s.name}</span>
                    <span className="block truncate text-caption text-muted-foreground" title={s.description ?? s.method ?? ""}>
                      {s.description ?? s.method ?? ""}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          )}
          {q.trim() === "" && !showAll && all.length > shown.length ? (
            <Button variant="ghost" size="sm" className="mt-1" onClick={() => setShowAll(true)}>
              Show all {all.length} specialists
            </Button>
          ) : null}
        </div>
        {picked.length > 0 ? <p className="text-caption">Picked: {names.join(", ")}</p> : null}
        {needFolder ? (
          <Label
            hint={
              folder.trim() === "" ? (
                <span className="text-danger">Choose a folder: the specialists you picked work in a repository.</span>
              ) : (
                "The repository the team works in. The lead alone needs none."
              )
            }
          >
            Work folder
            <div className="flex gap-2">
              <Input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="/Users/you/repos/project" className="font-mono" />
              <Button
                onClick={async () => {
                  if (localDesktop === undefined) return setBrowsing(true);
                  const p = await localDesktop.pickFolder();
                  if (p !== null && p !== undefined) setFolder(p);
                }}
              >
                <FolderOpen /> Choose…
              </Button>
            </div>
            {browsing ? (
              <FolderBrowser
                start={folder.trim() === "" ? "~" : folder}
                onPick={(p) => {
                  setFolder(p);
                  setBrowsing(false);
                }}
                onCancel={() => setBrowsing(false)}
              />
            ) : null}
          </Label>
        ) : null}
        <Label hint={options.length === 0 ? "None of the agents can run here." : "The lead and each specialist run on it."}>
          Agent
          <Select value={pick} onChange={(e) => setAgent(e.target.value)} disabled={options.length === 0}>
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
        </Label>
      </div>
    </Sheet>
  );
}
