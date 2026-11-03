import { ChevronDown, ChevronRight, BookOpen, Download, Loader2, Store, Brain, FileText, Folder, FolderOpen, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useEffect, useRef, useState, type ReactElement, type ReactNode } from "react";

import { AGENTS, harnessLabel, useApp, usePersisted } from "../app/ctx";
import { localDesktop } from "../app/desktop";
import { Button } from "../components/ui/button";
import { Badge, Empty, Input, Label, Select, Textarea } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import { cn } from "../lib/utils";
import { Codes } from "../protocol/errors";
import type { Skill } from "../protocol/domain";
import { Credits, MarketplaceSheet, useMarketplace } from "./Marketplace";
import { HelperExperience } from "./Experience";
import { Mascot } from "./Mascot";
import { ListDetail, PageHeader } from "./Page";
import { groupOnMap, groupSkills } from "./skillgroups";

// Skill library: registered skill repositories. Each entry's memory and task logs are kept by the
// host (agents.db), imported once from the repository. Registration starts no session.
// The Helper hub's helpers not installed yet are listed below them, each with Install; the map's
// Helper pool shows only the installed ones.

export function SkillLibrary(): ReactElement {
  const app = useApp();
  const skills = app.domain.skills;
  const [sel, setSel] = useState<string | null>(null);
  const current = skills.find((s) => s.id === sel) ?? skills[0];
  const [rev, setRev] = useState(0);
  const market = useMarketplace(rev);
  const available = (market?.skills ?? []).filter((e) => e.skill_id === undefined);
  const [busy, setBusy] = useState<string | null>(null);
  const install = async (name: string): Promise<void> => {
    setBusy(name);
    const r = await app.call("marketplace.install", { name });
    setBusy(null);
    if (r !== undefined) toast.success(`Installed ${name}`);
    setRev((n) => n + 1);
  };
  const setOnMap = (ss: Skill[], on: boolean): void => void Promise.all(ss.filter((s) => (s.on_map === true) !== on).map((s) => app.call("skill.update", { id: s.id, on_map: on })));
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title="Helpers"
        subtitle="Reusable agents with their own method, experience and history. Install one here and tick On the map for the ones you use; place them on a team from the map's Helper pool."
        action={
          <div className="flex gap-2">
            <Button onClick={() => app.showDialog(<MarketplaceSheet />)}>
              <Store /> Helper hub
            </Button>
            <Button
              variant="ghost"
              disabled={busy !== null}
              title="Pull the Helper hub's latest catalog"
              onClick={async () => {
                setBusy("refresh");
                await app.call("marketplace.refresh", {});
                setBusy(null);
                setRev((n) => n + 1);
              }}
            >
              <RefreshCw /> {busy === "refresh" ? "Pulling…" : "Refresh"}
            </Button>
            <Button variant="default" onClick={() => app.showDialog(<AddSkill />)}>
              <Plus /> Add helper
            </Button>
          </div>
        }
      />
      {skills.length === 0 && available.length === 0 ? (
        <Empty icon={<Mascot />} title="No helpers yet">
          Add one from the Helper hub, register an existing skill folder, add a Git source pinned to a commit, or create a new one from the skill-repo template.
        </Empty>
      ) : (
        <ListDetail
          id="skills"
          list={
            <>
              {skills.length > 0 ? <p className="px-3 pb-1 pt-2 text-caption font-medium text-muted-foreground">Installed · {skills.length}</p> : null}
              <HelperTree
                id="installed"
                label="Installed helpers"
                items={skills}
                groupOf={(s) => s.group ?? ""}
                holds={(s) => s.id === sel /* only a helper the owner picked, not the default one */}
                folderCheck={(items, group) => <MapCheck value={groupOnMap(items)} label={`On the map: every helper in ${group}`} onChange={(v) => setOnMap(items, v)} />}
                row={(s, nested) => (
                  <li key={s.id} className="flex items-start gap-1">
                    {/* A control of its own beside the row's button, never inside it. */}
                    <MapCheck value={s.on_map === true} label={`On the map: ${s.name}`} onChange={(v) => setOnMap([s], v)} className="mt-2" />
                    <button type="button" aria-current={current?.id === s.id ? "true" : undefined} onClick={() => setSel(s.id)} className={cn("grid min-w-0 flex-1 gap-0.5 rounded-sm px-2 py-1.5 text-left hover:bg-hover", current?.id === s.id && "bg-selected")}>
                      <span className="flex items-center gap-1 text-label font-medium">
                        {nested ? null : <span className="w-3.5 shrink-0" />}
                        <BookOpen className="size-3.5 shrink-0 text-faint" />
                        <span className="truncate">{s.name}</span>
                      </span>
                      <span className={cn("truncate text-caption text-muted-foreground", nested ? "pl-[18px]" : "pl-9")}>{(s.capabilities ?? []).join(", ") || "no capabilities"}</span>
                    </button>
                  </li>
                )}
              />
              {available.length > 0 ? <p className="px-3 pb-1 pt-4 text-caption font-medium text-muted-foreground">Available · {available.length}</p> : null}
              {market?.error !== undefined ? <p className="px-3 pt-4 text-caption text-danger">Helper hub unavailable: {market.error}</p> : null}
              <HelperTree
                id="available"
                label="Helpers available in the Helper hub"
                items={available}
                groupOf={(e) => e.category ?? ""}
                row={(e, nested) => (
                  <li key={e.name} className="grid gap-0.5 px-2 py-1.5">
                    <span className="flex items-center gap-1 text-label font-medium">
                      {nested ? null : <span className="w-3.5 shrink-0" />}
                      <BookOpen className="size-3.5 shrink-0 text-faint" />
                      <span className="min-w-0 flex-1 truncate">{e.name}</span>
                      <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void install(e.name)}>
                        {busy === e.name ? <Loader2 className="animate-spin" /> : <Download />} {busy === e.name ? "Installing…" : "Install"}
                      </Button>
                    </span>
                    <span className={cn("line-clamp-2 text-caption text-muted-foreground", nested ? "pl-[18px]" : "pl-9")}>{e.description ?? ((e.capabilities ?? []).join(", ") || "no capabilities")}</span>
                    <Credits credits={e.credits} className={nested ? "pl-[18px]" : "pl-9"} />
                  </li>
                )}
              />
            </>
          }
          detail={
            current === undefined ? (
              <Empty icon={<Mascot />} title="No helpers installed">
                Install one from the list, or add your own with Add helper.
              </Empty>
            ) : (
              <SkillDetail key={current.id} s={current} />
            )
          }
        />
      )}
    </div>
  );
}

/** "On the map": the helper is listed in the map's Helper pool. A folder's box is mixed when
 *  only some of its helpers are; ticking it puts them all on the map. */
function MapCheck({ value, label, onChange, className }: { value: boolean | "mixed"; label: string; onChange: (v: boolean) => void; className?: string }): ReactElement {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current !== null) ref.current.indeterminate = value === "mixed";
  }, [value]);
  return (
    <input
      ref={ref}
      type="checkbox"
      aria-label={label}
      title="On the map: listed in the map's Helper pool"
      checked={value === true}
      onChange={(e) => onChange(e.target.checked)}
      className={cn("shrink-0", className)}
    />
  );
}

/** One section of helpers as a folder tree, like the workspace Explorer: a folder per group (open by
 *  default, remembered per section and group; the one holding the selected helper stays open) with
 *  its helpers under a guide line, then the ungrouped helpers at the root. A plain list with
 *  disclosure buttons, not an ARIA tree. folderCheck, when given, is a control at each folder row's start. */
function HelperTree<T>({
  id,
  label,
  items,
  groupOf,
  holds,
  folderCheck,
  row,
}: {
  id: string;
  label: string;
  items: T[];
  groupOf: (t: T) => string;
  holds?: (t: T) => boolean;
  folderCheck?: (items: T[], group: string) => ReactNode;
  row: (t: T, nested: boolean) => ReactElement;
}): ReactElement | null {
  if (items.length === 0) return null;
  return (
    <ul aria-label={label} className="px-1">
      {groupSkills(items, groupOf).map(({ group, items }) =>
        group === "" ? (
          items.map((t) => row(t, false))
        ) : (
          <GroupFolder key={group} id={`${id}.${group}`} group={group} count={items.length} pinned={holds !== undefined && items.some(holds)} check={folderCheck?.(items, group)}>
            {items.map((t) => row(t, true))}
          </GroupFolder>
        ),
      )}
    </ul>
  );
}

/** A group's folder: a disclosure button (Enter and Space toggle it) over its helpers. */
function GroupFolder({ id, group, count, pinned, check, children }: { id: string; group: string; count: number; pinned: boolean; check?: ReactNode; children: ReactNode }): ReactElement {
  const [stored, setOpen] = usePersisted(`helpers.group.${id}`, true);
  const open = stored || pinned;
  const bodyId = `helpers-group-${id.replace(/[^\w-]/g, "-")}`;
  return (
    <li>
      <div className="flex items-center gap-1">
        {check}
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          aria-disabled={pinned || undefined}
          title={pinned ? "Holds the selected helper" : undefined}
          onClick={() => {
            if (!pinned) setOpen(!open);
          }}
          className="flex h-7 min-w-0 flex-1 items-center gap-1 rounded-sm px-2 text-left text-label text-muted-foreground hover:bg-hover hover:text-foreground [&_svg]:size-3.5 [&_svg]:shrink-0"
        >
          {open ? <ChevronDown /> : <ChevronRight />}
          {open ? <FolderOpen className="text-primary" /> : <Folder className="text-primary" />}
          <span className="truncate font-medium">{group}</span>
          <span className="ml-auto pl-2 text-micro tabular-nums text-faint">{count}</span>
        </button>
      </div>
      <ul id={bodyId} hidden={!open} className="ml-[15px] border-l border-border pl-1">
        {children}
      </ul>
    </li>
  );
}

function AddSkill(): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const [mode, setMode] = useState<"local" | "create" | "git">("local");
  const [f, setF] = useState({
    name: "",
    path: "",
    url: "",
    commit: "",
    method: "",
    description: "",
    caps: "",
    harness: "",
    task: "",
  });
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const submit = async (): Promise<void> => {
    setBusy(true);
    const r = await app.call<{ skill: { name: string } }>("skill.add", {
      name: f.name,
      path: f.path,
      url: mode === "git" ? f.url : undefined,
      commit: mode === "git" && f.commit !== "" ? f.commit : undefined,
      create: mode === "create",
      task: mode === "create" ? f.task : undefined,
      method: f.method,
      description: f.description,
      capabilities: f.caps === "" ? [] : f.caps.split(","),
      harness: f.harness,
    });
    setBusy(false);
    if (r !== undefined) close();
  };
  return (
    <Sheet
      title="Add helper"
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="default" disabled={busy || f.path === ""} onClick={() => void submit()}>
            {busy ? "Adding…" : "Add"}
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-1 gap-4">
        <div className="flex gap-1 rounded-lg bg-muted p-1 text-label">
          {(
            [
              ["local", "Existing folder"],
              ["create", "Create new"],
              ["git", "Git source"],
            ] as const
          ).map(([k, label]) => (
            <button key={k} type="button" onClick={() => setMode(k)} className={cn("h-7 flex-1 rounded-md", mode === k ? "bg-panel font-medium shadow-sm" : "text-muted-foreground")}>
              {label}
            </button>
          ))}
        </div>
        {mode === "git" ? (
          <div className="grid grid-cols-2 gap-3">
            <Label>
              Git URL
              <Input value={f.url} onChange={set("url")} className="font-mono" />
            </Label>
            <Label hint="Default: the cloned HEAD">
              Pinned commit
              <Input value={f.commit} onChange={set("commit")} className="font-mono" />
            </Label>
          </div>
        ) : null}
        <Label hint={mode === "create" ? "Must be empty or new; the skill-repo template is written there." : undefined}>
          {mode === "local" ? "Repository folder" : mode === "create" ? "New folder" : "Clone destination"}
          <div className="flex gap-2">
            <Input value={f.path} onChange={set("path")} className="font-mono" placeholder="/Users/you/repos/review-agent" />
            {localDesktop !== undefined ? (
              <Button
                onClick={async () => {
                  const p = await localDesktop?.pickFolder();
                  if (p !== null && p !== undefined) setF({ ...f, path: p });
                }}
              >
                <FolderOpen /> Choose…
              </Button>
            ) : null}
          </div>
        </Label>
        <div className="grid grid-cols-2 gap-3">
          <Label>
            Name
            <Input value={f.name} onChange={set("name")} placeholder="defaults to the folder" />
          </Label>
          {mode === "create" ? (
            <Label>
              Skill name
              <Input value={f.task} onChange={set("task")} placeholder="code-review" />
            </Label>
          ) : (
            <Label>
              Harness
              <Select value={f.harness} onChange={set("harness")}>
                <option value="">Any</option>
                {AGENTS.filter((a) => !a.hidden).map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.label}
                  </option>
                ))}
              </Select>
            </Label>
          )}
        </div>
        <Label>
          Method
          <Input value={f.method} onChange={set("method")} placeholder="What it does, in one line" />
        </Label>
        <Label hint="Comma separated; defaults to its skill names.">
          Capabilities
          <Input value={f.caps} onChange={set("caps")} placeholder="review, testing" />
        </Label>
      </div>
    </Sheet>
  );
}

function SkillDetail({ s }: { s: Skill }): ReactElement {
  const app = useApp();
  const [tab, setTab] = useState<"overview" | "experience">("overview");
  // What keeps a helper from being deleted; the host refuses the same.
  const team = app.domain.teams.find((t) => t.state === "active" && t.members.some((m) => m.skill_id === s.id));
  const running = app.sessions.find((x) => x.state === "running" && x.skill_id === s.id);
  const blocked = team !== undefined ? `On the active team ${team.name}: close the team first` : running !== undefined ? `Running in ${running.name}: end it first` : undefined;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-start gap-2 border-b border-border p-4">
        <div className="min-w-48 flex-1">
          <h2 className="truncate text-title font-semibold" title={s.name}>
            {s.name}
          </h2>
          <p className="truncate font-mono text-caption text-muted-foreground">
            {s.path}
            {s.commit !== undefined && s.commit !== "" ? ` @ ${s.commit.slice(0, 10)}` : ""}
          </p>
          {/* Helpers are harness-agnostic; a pin to one agent is the exception, so it shows. */}
          {(s.harness ?? "") !== "" || (s.requires ?? []).length > 0 ? (
            <p className="mt-1 flex flex-wrap gap-1">
              {s.harness !== undefined && s.harness !== "" ? <Badge tone="info">Prefers {harnessLabel(s.harness)}</Badge> : null}
              {(s.requires ?? []).length > 0 ? <Badge tone="warning">Only runs on {(s.requires ?? []).map(harnessLabel).join(" or ")}</Badge> : null}
            </p>
          ) : null}
        </div>
        <Button size="sm" variant="ghost" onClick={() => localDesktop?.revealPath(s.path)} disabled={localDesktop === undefined}>
          <FolderOpen /> Reveal
        </Button>
        <Button size="sm" variant="danger" disabled={blocked !== undefined} title={blocked ?? "Delete the agent for good"} onClick={() => app.showDialog(<DeleteSkillSheet s={s} />)}>
          <Trash2 /> Delete
        </Button>
        {blocked !== undefined ? <span className="text-caption text-muted-foreground">{blocked}</span> : null}
      </div>
      <div className="flex gap-4 border-b border-border px-4">
        {(
          [
            ["overview", "Overview", <FileText key="o" />],
            ["experience", "Experience", <Brain key="m" />],
          ] as const
        ).map(([k, label, icon]) => (
          <button
            key={k}
            type="button"
            onClick={() => setTab(k)}
            aria-selected={tab === k}
            role="tab"
            className={cn(
              "-mb-px flex items-center gap-1.5 border-b-2 py-2 text-label [&_svg]:size-3.5",
              tab === k ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {icon}
            {label}
          </button>
        ))}
      </div>
      {tab === "overview" ? (
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <Overview s={s} />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <HelperExperience skill={s} />
        </div>
      )}
    </div>
  );
}

interface DeletePlan {
  name: string;
  folder: string;
  folder_kept: string;
  entries: number;
}

/** Confirms deleting a helper: its entry, its journal and the clone PenguPool made. */
function DeleteSkillSheet({ s }: { s: Skill }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  const [plan, setPlan] = useState<DeletePlan | null>(null);
  const [found, setFound] = useState<string | null>(null); // the owner's work in the clone, from owner_work
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void app.call<DeletePlan>("skill.delete", { id: s.id, dry_run: true }).then((r) => (r === undefined ? close() : setPlan(r)));
  }, [s.id]);
  // confirm: the owner_work answer the owner saw; the host deletes only while that is still all there is.
  const remove = async (confirm?: string): Promise<void> => {
    setBusy(true);
    try {
      const r = await app.conn.request<{ deleted: string; folder_kept?: string }>("skill.delete", { id: s.id, confirm_owner_work: confirm });
      toast.success(`Deleted ${r.deleted}${r.folder_kept !== undefined ? `; ${r.folder_kept}` : ""}`);
      close();
    } catch (e) {
      // The connection rejects with the daemon's message and its code; a new list replaces the one shown.
      if ((e as { code?: string }).code === Codes.OwnerWork) setFound((e as Error).message.replace(`${Codes.OwnerWork}: `, ""));
      else toast.error((e as Error).message);
      setBusy(false);
    }
  };
  const n = (k: number, one: string, many: string): string => `${k} ${k === 1 ? one : many}`;
  return (
    <Sheet
      title={`Delete ${s.name}?`}
      description={
        plan === null
          ? "Checking what it holds…"
          : `This removes its entry, its ${n(plan.entries, "journal entry", "journal entries")}${
              plan.folder !== "" ? `, and its folder ${plan.folder}` : `; ${plan.folder_kept}`
            }. This can't be undone.`
      }
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Keep it
          </Button>
          {found !== null ? (
            <Button variant="danger" disabled={busy} onClick={() => void remove(found)}>
              Delete anyway
            </Button>
          ) : (
            <Button variant="danger" disabled={busy || plan === null} onClick={() => void remove()}>
              {busy ? "Deleting…" : "Delete"}
            </Button>
          )}
        </>
      }
    >
      {found !== null ? (
        <div className="rounded-md bg-danger/10 p-3 text-label">
          <p className="font-medium text-danger">Its folder holds work of yours that would be lost</p>
          <pre className="mt-1 whitespace-pre-wrap break-all font-mono text-caption text-muted-foreground">{found}</pre>
        </div>
      ) : null}
    </Sheet>
  );
}

function Overview({ s }: { s: Skill }): ReactElement {
  const app = useApp();
  const [edit, setEdit] = useState(false);
  const [f, setF] = useState({
    method: s.method ?? "",
    description: s.description ?? "",
    caps: (s.capabilities ?? []).join(", "),
    harness: s.harness ?? "",
    requires: s.requires ?? [],
    group: s.group ?? "",
  });
  const market = useMarketplace(0);
  const groups = [...new Set([...app.domain.skills.map((x) => x.group ?? ""), ...(market?.skills ?? []).map((e) => e.category ?? "")].filter((g) => g !== ""))].sort();
  const served = app.domain.teams.filter((t) => t.members.some((m) => m.skill_id === s.id));
  return (
    <div className="grid grid-cols-1 max-w-3xl gap-5">
      <div className="rounded-md border border-border p-3 text-label">
        <p className="font-medium">Agent</p>
        <p className="mt-1 text-muted-foreground">
          Working since {s.created_at.slice(0, 10)} · identity <span className="font-mono text-caption">{s.key ?? s.id}</span> · served {served.length}{" "}
          {served.length === 1 ? "team" : "teams"}
        </p>
        <p className="mt-1 text-caption text-faint">The skill files are where this agent started. Its experience is its own: it grows with every job, follows the helper on every machine, and survives upgrades and reinstalls.</p>
      </div>
      <p className="text-caption text-faint">The agent reads its skills but never changes them; what it learns goes to its experience in PenguPool.</p>
      {edit ? (
        <div className="grid grid-cols-1 gap-3">
          <Label>
            Method
            <Input value={f.method} onChange={(e) => setF({ ...f, method: e.target.value })} />
          </Label>
          <Label>
            Description
            <Textarea value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
          </Label>
          <Label>
            Capabilities
            <Input value={f.caps} onChange={(e) => setF({ ...f, caps: e.target.value })} />
          </Label>
          <Label>
            Group
            <Input list="helper-groups" value={f.group} maxLength={40} onChange={(e) => setF({ ...f, group: e.target.value })} />
            <datalist id="helper-groups">
              {groups.map((g) => (
                <option key={g} value={g} />
              ))}
            </datalist>
          </Label>
          <Label>
            Preferred agent
            <Select value={f.harness} onChange={(e) => setF({ ...f, harness: e.target.value })}>
              <option value="">Any</option>
              {AGENTS.filter((a) => !a.hidden).map((a) => (
                <option key={a.id} value={a.id}>
                  {a.label}
                </option>
              ))}
            </Select>
          </Label>
          <fieldset className="grid grid-cols-1 gap-1 text-label">
            <legend className="mb-1 font-medium">Only runs on</legend>
            <p className="text-caption text-faint">Leave all unchecked when any agent can run it.</p>
            <div className="flex flex-wrap gap-3">
              {AGENTS.filter((a) => !a.hidden).map((a) => (
                <label key={a.id} className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    checked={f.requires.includes(a.id)}
                    onChange={(e) => setF({ ...f, requires: e.target.checked ? [...f.requires, a.id] : f.requires.filter((x) => x !== a.id) })}
                  />
                  {a.label}
                </label>
              ))}
            </div>
          </fieldset>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setEdit(false)}>
              Cancel
            </Button>
            <Button
              variant="default"
              onClick={async () => {
                await app.call("skill.update", {
                  id: s.id,
                  method: f.method,
                  description: f.description,
                  capabilities: f.caps.split(","),
                  harness: f.harness,
                  requires: f.requires,
                  group: f.group.trim(),
                });
                setEdit(false);
              }}
            >
              Save
            </Button>
          </div>
        </div>
      ) : (
        <dl className="grid grid-cols-[140px_1fr] gap-x-4 gap-y-2 text-label">
          <dt className="text-muted-foreground">Method</dt>
          <dd>{s.method || "—"}</dd>
          <dt className="text-muted-foreground">Description</dt>
          <dd>
            {s.description || "—"}
            <Credits credits={s.credits} className="mt-0.5" />
          </dd>
          <dt className="text-muted-foreground">Group</dt>
          <dd>{s.group || "—"}</dd>
          <dt className="text-muted-foreground">Capabilities</dt>
          <dd className="flex flex-wrap gap-1">
            {(s.capabilities ?? []).length > 0
              ? (s.capabilities ?? []).map((c) => (
                  <Badge key={c} tone="primary">
                    {c}
                  </Badge>
                ))
              : "—"}
          </dd>
          <dt className="text-muted-foreground">Preferred agent</dt>
          <dd>{s.harness !== undefined && s.harness !== "" ? harnessLabel(s.harness) : "any"}</dd>
          <dt className="text-muted-foreground">Only runs on</dt>
          <dd>{(s.requires ?? []).length > 0 ? (s.requires ?? []).map(harnessLabel).join(" or ") : "any agent"}</dd>
          <dt className="text-muted-foreground">Profiles</dt>
          <dd>{(s.profiles ?? []).join(", ") || "—"}</dd>
          <dd className="col-start-2">
            <Button size="sm" onClick={() => setEdit(true)}>
              Edit
            </Button>
          </dd>
        </dl>
      )}
      <div>
        <h3 className="mb-2 text-label font-semibold">Skills</h3>
        <div className="grid grid-cols-1 gap-2">
          {(s.files ?? []).map((f) => (
            <div key={f.name} className="rounded-md border border-border p-3">
              <div className="flex items-center gap-2">
                <span className="font-medium">{f.name}</span>
                <span className="font-mono text-micro text-faint">{f.digest}</span>
                {f.problem !== undefined ? <Badge tone="danger">{f.problem}</Badge> : null}
              </div>
              <p className="mt-1 text-caption text-muted-foreground">{f.description}</p>
            </div>
          ))}
          {(s.files ?? []).length === 0 ? <p className="text-caption text-muted-foreground">No skills/&lt;name&gt;/SKILL.md found.</p> : null}
        </div>
      </div>
    </div>
  );
}
