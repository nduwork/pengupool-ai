import { Bot, Check, GitBranch, KeyRound, Loader2, Lock, MonitorUp, RefreshCw, Server, Settings2, Smartphone, Sparkles, Terminal, Waypoints, X } from "lucide-react";
import { useEffect, useState, type ReactElement, type ReactNode } from "react";
import { toast } from "sonner";

import { AGENTS, useApp, usePersisted, type Found } from "../app/ctx";
import { desktop, localDesktop } from "../app/desktop";
import { Button } from "../components/ui/button";
import { SCROLLBACK_DEFAULT, clampScrollback } from "../components/termCache";
import { Badge, Card, Input, Label, Select, Switch } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import type { NodeView, Update } from "../protocol/domain";
import { cn, shortTime } from "../lib/utils";
import { GitHubAccount } from "./GitHub";
import { UpdateSheet } from "./Update";
import { NewSessionDialog } from "./NewSession";
import { PageHeader } from "./Page";
import { contributorNote, formatName, type RemovedModel } from "./triage";

// Settings, one subpanel per topic: General (the host, updates, terminal history, the daily backup),
// Agents (which are installed, Claude Code on Ollama), Decision model, GitHub (the account and sync),
// Tailscale, Muse app and SSH hosts.

// [id, label, icon]
const SECTIONS: Array<[string, string, ReactElement]> = [
  ["general", "General", <Settings2 key="s" />],
  ["agents", "Agents", <Terminal key="a" />],
  ["models", "Decision model", <Sparkles key="p" />],
  ["github", "GitHub", <GitBranch key="g" />],
  ["tailscale", "Tailscale", <Waypoints key="t" />],
  ["muse", "Muse app", <Smartphone key="m" />],
  ["nodes", "SSH hosts", <Server key="n" />],
];

export function Settings(): ReactElement {
  const [saved, setSection] = usePersisted("settings.section", "general");
  // A section remembered from an older version may be gone.
  const section = SECTIONS.some(([id]) => id === saved) ? saved : "general";
  return (
    <div className="flex h-full flex-col">
      <PageHeader title="Settings" />
      <div className="flex min-h-0 flex-1">
        <nav className="w-56 shrink-0 space-y-px border-r border-border p-2">
          {SECTIONS.map(([id, label, icon]) => (
            <button
              key={id}
              type="button"
              onClick={() => setSection(id)}
              aria-current={section === id}
              className={cn(
                "flex h-8 w-full items-center gap-2 rounded-md px-2.5 text-label text-muted-foreground hover:bg-hover [&_svg]:size-4",
                section === id && "bg-selected font-medium text-foreground",
              )}
            >
              {icon}
              {label}
            </button>
          ))}
        </nav>
        <div className="min-w-0 flex-1 overflow-auto">
          <div className="mx-auto max-w-3xl p-6">
            {section === "general" ? <General /> : null}
            {section === "agents" ? <Agents /> : null}
            {section === "models" ? <Providers /> : null}
            {section === "github" ? <GitHub /> : null}
            {section === "tailscale" ? <Tailscale /> : null}
            {section === "muse" ? <MuseApp /> : null}
            {section === "nodes" ? <Nodes /> : null}
          </div>
        </div>
      </div>
    </div>
  );
}

function H({ title, children }: { title: string; children?: ReactNode }): ReactElement {
  return (
    <div className="mb-4">
      <h2 className="text-title font-semibold">{title}</h2>
      {children !== undefined ? <p className="mt-1 text-label text-muted-foreground">{children}</p> : null}
    </div>
  );
}

interface ProviderCfg {
  type: string;
  format: string;
  name?: string;
  address: string;
  model?: string;
  key_ref?: string;
}

interface ProvidersResp {
  active: string;
  providers: Record<string, ProviderCfg> | null;
  /** The provider types whose saved settings passed the host's last connection check. */
  checked?: string[];
  /** Saved models the host removed because triage no longer speaks their format. */
  removed?: RemovedModel[] | null;
  defaults: Record<string, ProviderCfg>;
}

// Two kinds of decision model: local hosted (Ollama) and online (Jev, in the cloud). The third tab is
// Muse Spark's task check (SparkPane), never the triage model.
const TYPES = [
  { id: "ollama", label: "Local hosted" },
  { id: "jev", label: "Online / cloud" },
  { id: "meta", label: "Meta · Muse Spark" },
];

interface SparkCfg {
  /** Spark is on unless the owner turns it off. */
  off?: boolean;
  address: string;
  model?: string;
  key_ref?: string;
}

interface SparkCheck {
  ok: boolean;
  error?: string;
  model?: string;
  pick?: string;
  reasoning?: string;
  latency_ms?: number;
}

/** spark.get and spark.save: the settings, and whether Spark is active (on and set up). */
interface SparkResp {
  spark: SparkCfg;
  active: boolean;
}

/** The Meta · Muse Spark tab: the Meta Model API's settings, with the same buttons as the triage tabs; Use turns its task check on. */
function SparkPane({ onSaved }: { onSaved: () => void }): ReactElement {
  const app = useApp();
  const [saved, setSaved] = useState<SparkCfg | null>(null);
  const [draft, setDraft] = useState<SparkCfg | null>(null);
  const [active, setActive] = useState(false);
  const [key, setKey] = useState("");
  const [check, setCheck] = useState<SparkCheck | "running" | null>(null);
  useEffect(() => {
    void app.call<SparkResp>("spark.get").then((r) => {
      if (r === undefined) return;
      setSaved(r.spark);
      setDraft(r.spark);
      setActive(r.active);
    });
  }, []);
  if (draft === null || saved === null) return <p className="text-label text-faint">Loading…</p>;
  const set = (k: keyof SparkCfg, v: string): void => setDraft({ ...draft, [k]: v });
  const save = async (next: SparkCfg): Promise<void> => {
    const r = await app.call<SparkResp>("spark.save", { spark: next, key: key === "" ? undefined : key });
    if (r === undefined) return;
    setKey("");
    setSaved(r.spark);
    setDraft(r.spark);
    setActive(r.active);
    setCheck(null);
    onSaved();
    toast.success("Saved.");
  };
  const unchanged = JSON.stringify(draft) === JSON.stringify(saved) && key === "";
  const off = saved.off === true;
  // The same steps as the triage tabs: Save → Check connection → Use; none while in use.
  const failed = check !== null && check !== "running" && !check.ok;
  const next = !unchanged ? "save" : failed ? "check" : off ? "use" : "";
  return (
    <div className="grid grid-cols-1 gap-4">
      <Label hint="The Meta Model API. PenguPool calls its chat completions, /chat/completions.">
        Address
        <Input value={draft.address} onChange={(e) => set("address", e.target.value)} className="font-mono" />
      </Label>
      <div className="grid grid-cols-2 gap-3">
        <Label hint={saved.key_ref?.startsWith("keychain:") === true ? "A key is saved in the macOS Keychain." : "Required. Stored in the macOS Keychain, never in settings."}>
          API key
          <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" />
        </Label>
        <Label hint="Or reference an environment variable of the host.">
          Key reference
          <Input value={draft.key_ref?.startsWith("env:") === true ? draft.key_ref : ""} onChange={(e) => set("key_ref", e.target.value)} placeholder="env:META_API_KEY" className="font-mono" />
        </Label>
      </div>
      <Label hint="Required: the Meta Model API's model string. Reasoning effort: minimal (fixed for triage).">
        Model
        <Input value={draft.model ?? ""} onChange={(e) => set("model", e.target.value)} className="font-mono" placeholder="muse-spark-1.3" />
      </Label>
      {contributorNote(draft.model) !== null ? <p className="text-caption text-warning">{contributorNote(draft.model)}</p> : null}
      <div className="flex flex-wrap gap-2">
        <Button variant={next === "save" ? "default" : "secondary"} onClick={() => void save(draft)}>
          Save provider
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            setDraft(saved);
            setKey("");
          }}
        >
          Discard changes
        </Button>
        <Button
          variant={next === "check" ? "default" : "secondary"}
          disabled={!unchanged || (saved.model ?? "") === "" || check === "running"}
          title="Asks the saved model who should get a sample task"
          onClick={async () => {
            setCheck("running");
            setCheck((await app.call<SparkCheck>("spark.check")) ?? null);
          }}
        >
          {check === "running" ? (
            <>
              <Loader2 className="size-3.5 animate-spin" />
              Checking…
            </>
          ) : (
            "Check connection"
          )}
        </Button>
        <Button variant={next === "use" ? "default" : "secondary"} disabled={!off || !unchanged} onClick={() => void save({ ...saved, off: false })}>
          Use for send-path triage
        </Button>
        {!off ? (
          <Button variant="ghost" disabled={!unchanged} onClick={() => void save({ ...saved, off: true })}>
            Stop using
          </Button>
        ) : null}
      </div>
      <p role="status" className={cn("text-caption", failed ? "text-danger" : "text-muted-foreground")}>
        {!unchanged
          ? "Unsaved changes. Save them, or Discard changes to go back to the saved settings."
          : failed
            ? "The check failed. Fix the settings, save, and check again."
            : off
              ? "Not in use: tasks go as addressed, unchecked; System One's per-turn route hint applies."
              : active
                ? "In use for send-path triage: Muse Spark picks who gets each task a lead or builder sends."
                : "In use once set up: add the model and an API key. Until then tasks go unchecked and System One's per-turn route hint applies."}
      </p>
      {check !== null && check !== "running" ? (
        <Card className={cn("p-3 text-label", check.ok ? "border-success/40" : "border-danger/40")}>
          {check.ok ? (
            <span className="text-success">
              Answered · {check.model ?? ""} · {check.latency_ms ?? 0} ms: picked {check.pick} for a sample task. {check.reasoning}
            </span>
          ) : (
            <span className="text-danger">{check.error}</span>
          )}
        </Card>
      ) : null}
    </div>
  );
}

/** The provider's models, listed by the provider itself (with the typed or saved key). With a capability,
 * only the Ollama models that declare it (Ollama's own /api/show), and only those can be picked. */
function ProviderModel({
  type,
  typedKey,
  hasKey,
  value,
  onChange,
  address,
  capability,
  noun = "models",
  hint,
  quietError,
}: {
  type: string;
  typedKey: string;
  hasKey: boolean;
  value: string;
  onChange: (m: string) => void;
  address?: string;
  capability?: string;
  noun?: string;
  hint?: string;
  /** Shown in place of a listing error, when this provider is neither the saved one nor just checked. */
  quietError?: string;
}): ReactElement {
  const app = useApp();
  const [models, setModels] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    setModels(null);
    setError("");
    if (!hasKey) {
      setModels([]);
      return;
    }
    const t = window.setTimeout(() => {
      app.conn.request<string[]>("provider.models", { type, key: typedKey === "" ? undefined : typedKey, address, capability }).then(setModels, (e: Error) => {
        setModels([]);
        setError(e.message.replace(/^unavailable: /, ""));
      });
    }, 400);
    return () => window.clearTimeout(t);
  }, [type, typedKey, hasKey, n, address, capability]);
  const list = models ?? [];
  const strict = capability !== undefined;
  return (
    <Label
      hint={
        error !== ""
          ? (quietError ?? error)
          : models === null
            ? "Finding models…"
            : list.length === 0
              ? strict
                ? (hint ?? `No ${noun} found.`)
                : hasKey
                  ? "The provider listed no models; type one."
                  : "Add the API key above to choose from the provider's models."
              : `${list.length} ${list.length === 1 ? noun.replace(/s$/, "") : noun} found.`
      }
    >
      <span className="flex items-center">
        Model
        <button type="button" onClick={() => setN(n + 1)} className="ml-auto rounded p-0.5 text-faint hover:bg-hover hover:text-foreground" aria-label="Reload models">
          <RefreshCw className="size-3.5" />
        </button>
      </span>
      {list.length > 0 || strict ? (
        <Select value={value} onChange={(e) => onChange(e.target.value)} className="font-mono" disabled={list.length === 0}>
          <option value="">{list.length === 0 ? "None to pick" : "Pick a model"}</option>
          {value !== "" && !list.includes(value) ? (
            <option value={value}>
              {value}
              {strict && models !== null ? " (not offered)" : ""}
            </option>
          ) : null}
          {list.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </Select>
      ) : (
        <Input value={value} onChange={(e) => onChange(e.target.value)} className="font-mono" placeholder="model id" />
      )}
    </Label>
  );
}

interface CheckItem {
  name: string;
  want: string;
  got: string;
  ok: boolean;
}

interface CheckResp {
  ok: boolean;
  error?: string;
  hint?: string;
  model?: string;
  latency_ms?: number;
  calibrated?: boolean;
  checks?: CheckItem[];
}

/** A connection check's result: each graded job, what a fit model gives and what this one gave. */
function CheckResult({ r, ok }: { r: CheckResp | "running" | null; ok: string }): ReactElement | null {
  if (r === null) return null;
  if (r === "running") return <p className="text-label text-faint">Checking…</p>;
  return (
    <Card className={cn("p-3 text-label", r.ok ? "border-success/40" : "border-danger/40")}>
      {r.ok ? <span className="text-success">{ok}</span> : <span className="text-danger">{r.error}</span>}
      {r.hint !== undefined && r.hint !== "" ? <span className="block text-muted-foreground">{r.hint}</span> : null}
      {(r.checks ?? []).map((c) => (
        <span key={c.name} className="mt-1 flex items-start gap-2 text-caption">
          {c.ok ? <Check className="mt-0.5 size-3 shrink-0 text-success" /> : <X className="mt-0.5 size-3 shrink-0 text-danger" />}
          <span className="min-w-0 break-words">
            <span className="font-medium">{c.name}</span>: {c.got}
            {c.ok ? "" : ` (wanted ${c.want})`}
          </span>
        </span>
      ))}
    </Card>
  );
}

function Providers(): ReactElement {
  const app = useApp();
  const [data, setData] = useState<ProvidersResp | null>(null);
  const [type, setType] = useState("ollama");
  // One draft per provider type; selecting a type never activates it.
  const [drafts, setDrafts] = useState<Record<string, ProviderCfg>>({});
  const [key, setKey] = useState("");
  const [check, setCheck] = useState<CheckResp | "running" | null>(null);
  const [n, setN] = useState(0);
  const [sparkOn, setSparkOn] = useState(false);
  useEffect(() => {
    void app.call<ProvidersResp>("decision.providers").then((r) => {
      if (r === undefined) return;
      setData(r);
      const d: Record<string, ProviderCfg> = {};
      for (const t of TYPES) d[t.id] = r.providers?.[t.id] ?? r.defaults[t.id];
      setDrafts(d);
    });
    void app.call<SparkResp>("spark.get").then((r) => setSparkOn(r?.active === true));
  }, [n]);
  if (data === null) return <p className="text-label text-faint">Loading…</p>;
  const draft = drafts[type];
  const saved = data.providers?.[type];
  const unchanged = saved !== undefined && JSON.stringify(draft) === JSON.stringify(saved);
  const passed = unchanged && (data.checked ?? []).includes(type);
  const set = (k: keyof ProviderCfg, v: string): void => setDrafts({ ...drafts, [type]: { ...draft, [k]: v } });
  // Where the saved settings stand; the first match wins. It picks the status line and the one default button.
  const state =
    saved === undefined
      ? "unsaved"
      : !unchanged
        ? "dirty"
        : check === "running"
          ? "checking"
          : check !== null && !check.ok
            ? "failed"
            : data.active === type
              ? "inuse"
              : passed
                ? "passed"
                : "unchecked";
  const next = state === "unsaved" || state === "dirty" ? "save" : state === "failed" || state === "unchecked" ? "check" : state === "passed" ? "use" : "";
  const status: Record<typeof state, ReactNode> = {
    unsaved: "Save these settings, then check the connection.",
    dirty: "Unsaved changes. Save them and check again, or Discard changes to go back to the saved settings.",
    checking: "Checking the saved settings: a yes/no, a pick-one and a score question…",
    failed: "The check failed, so Use for triage stays off. Fix the settings, save, and check again.",
    unchecked: "Not checked since the last save. Use for triage turns on once Check connection passes.",
    passed: (
      <>
        <Check className="mr-1 inline size-3.5 align-[-2px]" />
        Check passed for the saved settings.
      </>
    ),
    inuse: "In use for triage.",
  };
  return (
    <div>
      <H title="Decision model (triage)">
        Answers PenguPool&apos;s yes/no and pick-one questions: where an owner&apos;s rule belongs and whether a check-in may go on. It never writes text. With an online decision model, each owner prompt (redacted) and choice is sent to it to learn your preferences; classification never delays an agent.
      </H>
      {(data.removed ?? []).map((m) => (
        <p key={m.type} className="mb-4 rounded-md bg-warning/10 p-3 text-caption text-warning">
          <strong>{TYPES.find((t) => t.id === m.type)?.label ?? m.type} model removed.</strong> {m.model ? `"${m.model}" used` : "It used"} the{" "}
          {formatName(m)}, which triage no longer supports.{" "}
          {m.type === "ollama" ? "Pull a System One decision model in Ollama, pick it below, save, check, and use it for triage." : "Set up a System One model below, save, check, and use it for triage."}
        </p>
      ))}
      <Card className="mb-4 flex items-center gap-3 p-3 text-label">
        <Bot className="size-4 text-primary" />
        Active: <strong>{data.active === "" ? "None yet (required)" : TYPES.find((t) => t.id === data.active)?.label}</strong>
      </Card>
      {data.active === "" ? (
        <p className="-mt-2 mb-4 text-label text-warning">
          PenguPool needs a triage model: its memory, check-ins and team proposals rely on it. Set up a local one (Ollama) or a remote one, run its check, and make it active.
        </p>
      ) : null}
      <div className="mb-4 flex gap-1 rounded-lg bg-muted p-1 text-label">
        {TYPES.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => {
              setType(t.id);
              setCheck(null);
              setKey("");
            }}
            className={cn("h-7 flex-1 whitespace-nowrap rounded-md px-2", type === t.id ? "bg-panel font-medium shadow-sm" : "text-muted-foreground")}
          >
            {t.label}
            {data.active === t.id || (t.id === "meta" && sparkOn) ? " ✓" : ""}
          </button>
        ))}
      </div>
      {type === "meta" ? (
        <SparkPane onSaved={() => setN(n + 1)} />
      ) : (
      <div className="grid grid-cols-1 gap-4">
        <Label hint={type === "ollama" ? "The Ollama address. PenguPool calls its System One endpoint, /v1/systemone." : "The full endpoint URL."}>
          Address
          <Input value={draft.address} onChange={(e) => set("address", e.target.value)} className="font-mono" />
        </Label>
        {type !== "ollama" ? (
          <div className="grid grid-cols-2 gap-3">
            <Label hint={saved?.key_ref?.startsWith("keychain:") === true ? "A key is saved in the macOS Keychain." : "Stored in the macOS Keychain, never in settings."}>
              API key
              <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" />
            </Label>
            <Label hint="Or reference an environment variable of the host.">
              Key reference
              <Input
                value={draft.key_ref?.startsWith("env:") === true ? draft.key_ref : ""}
                onChange={(e) => set("key_ref", e.target.value)}
                placeholder={`env:${type.toUpperCase()}_API_KEY`}
                className="font-mono"
              />
            </Label>
          </div>
        ) : null}
        {type === "ollama" ? (
          <ProviderModel
            type={type}
            typedKey={key}
            hasKey
            address={draft.address}
            value={draft.model ?? ""}
            onChange={(m) => set("model", m)}
            capability="decision"
            noun="decision models"
            quietError={data.active === "ollama" || check !== null ? undefined : "Ollama isn't running on this Mac."}
            hint="Ollama lists no model that declares the decision capability. Pull a System One decision model, then reload."
          />
        ) : (
          <Label hint="Any model string.">
            Model
            <Input value={draft.model ?? ""} onChange={(e) => set("model", e.target.value)} className="font-mono" />
          </Label>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant={next === "save" ? "default" : "secondary"}
            onClick={async () => {
              const r = await app.call("provider.save", {
                provider: { ...draft, type },
                key: key === "" ? undefined : key,
              });
              if (r !== undefined) {
                setKey("");
                setCheck(null); // the result described the settings before this save
                setN(n + 1);
                toast.success("Saved. Run the connection check before relying on it.");
              }
            }}
          >
            Save provider
          </Button>
          <Button variant="ghost" onClick={() => setDrafts({ ...drafts, [type]: saved ?? data.defaults[type] })}>
            Discard changes
          </Button>
          <Button
            variant={next === "check" ? "default" : "secondary"}
            disabled={!unchanged || state === "checking"}
            title="Asks the saved model a yes/no, a pick-one and a score question with plain answers, and grades its replies"
            onClick={async () => {
              setCheck("running");
              setCheck((await app.call<CheckResp>("provider.check", { type })) ?? null);
              setN(n + 1); // the host now holds whether these settings passed
            }}
          >
            {state === "checking" ? (
              <>
                <Loader2 className="size-3.5 animate-spin" />
                Checking…
              </>
            ) : (
              "Check connection"
            )}
          </Button>
          <Button
            variant={next === "use" ? "default" : "secondary"}
            disabled={!passed || data.active === type}
            onClick={async () => {
              await app.call("provider.activate", { type });
              setN(n + 1);
            }}
          >
            Use for triage
          </Button>
          {/* No "stop using a model": PenguPool needs one; switch to another instead. */}
        </div>
        <p role="status" className={cn("text-caption", state === "failed" ? "text-danger" : state === "passed" ? "text-success" : "text-muted-foreground")}>
          {status[state]}
        </p>
        <CheckResult
          r={unchanged && check !== "running" ? check : null}
          ok={check !== null && check !== "running" ? `Fit to decide · ${check.model ?? ""} · ${check.latency_ms ?? 0} ms` : ""}
        />
        <p className="text-caption text-faint">PenguPool never installs Ollama or pulls weights. Thresholds are calibration candidates, not validated values.</p>
      </div>
      )}
    </div>
  );
}

interface SettingsResp {
  network: { tailscale?: string };
  bound?: string;
  tailscale_name?: string;
  tailscale_error?: string;
  store: string;
  executable: string;
  version?: string;
}

function useSettings(n: number): SettingsResp | null {
  const app = useApp();
  const [s, setS] = useState<SettingsResp | null>(null);
  useEffect(() => {
    void app.call<SettingsResp>("settings.get").then((r) => setS(r ?? null));
  }, [n]);
  return s;
}

function Row({ title, hint, children }: { title: ReactNode; hint?: ReactNode; children: ReactNode }): ReactElement {
  return (
    <div className="flex items-start gap-4 border-b border-border py-3 last:border-0">
      <div className="min-w-0 flex-1">
        <p className="text-label font-medium">{title}</p>
        {hint !== undefined ? <p className="text-caption text-muted-foreground">{hint}</p> : null}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

const SCROLLBACK_CHOICES = [5000, 10000, 20000, 50000];

/** Welcome: whether it shows when the Workspace is empty (this window only), and opening it now. */
function WelcomeSettings(): ReactElement {
  const app = useApp();
  return (
    <Card className="mb-4 px-4">
      <Row title="Show Welcome when Workspace is empty" hint="Saved in this window, not on the host.">
        <Switch checked={app.welcome.show} onCheckedChange={app.welcome.setShow} label="Show Welcome when Workspace is empty" />
      </Row>
      <Row title="Welcome" hint="Start a team or a session, and see what setup is left.">
        <Button size="sm" onClick={app.welcome.open}>
          Open Welcome
        </Button>
      </Row>
    </Card>
  );
}

/** How far back a terminal scrolls. Per viewer: it lives in this browser or app window, not in the host. */
function TerminalHistory(): ReactElement {
  const [lines, setLines] = usePersisted<number>("terminal.scrollback", SCROLLBACK_DEFAULT);
  const value = SCROLLBACK_CHOICES.includes(lines) ? lines : SCROLLBACK_DEFAULT;
  return (
    <div className="mb-4 rounded-lg border border-border px-4">
      <Row
        title="Terminal history"
        hint="Lines each terminal keeps to scroll back through. A line costs about 12 bytes per column (about 30 MB for 20,000 lines of 120 columns); the last 4 sessions you left keep theirs, so up to five terminals at once. Applies when a terminal next opens."
      >
        <Select value={value} onChange={(e) => setLines(clampScrollback(Number(e.target.value)))} aria-label="Terminal history">
          {SCROLLBACK_CHOICES.map((n) => (
            <option key={n} value={n}>
              {n.toLocaleString("en-US")} lines
            </option>
          ))}
        </Select>
      </Row>
    </div>
  );
}

/** Tailscale: Serve over the owner's tailnet, and the setup checklist. */
const HTTP_WARNING = "Plain HTTP over the tailnet: traffic is WireGuard-encrypted but not TLS. Use HTTPS if your tailnet has certificates.";

function Tailscale(): ReactElement {
  const app = useApp();
  const [n, setN] = useState(0);
  const [switching, setSwitching] = useState(false); // one Serve switch at a time: a second click would queue behind it and time out
  const s = useSettings(n);
  if (s === null) return <p className="text-label text-faint">Loading…</p>;
  const serve = async (params: Record<string, unknown>, done: (r: Record<string, string>) => void): Promise<void> => {
    setSwitching(true);
    try {
      const r = await app.call<Record<string, string>>("settings.save", params);
      if (r !== undefined) done(r);
    } finally {
      setSwitching(false);
      setN((x) => x + 1);
    }
  };
  return (
    <div>
      <H title="Tailscale">Paired devices reach this machine anywhere, over your private tailnet. PenguPool never uses Funnel.</H>
      <Card className="mb-4 px-4">
        <Row title="Tailscale" hint={s.tailscale_name !== undefined ? `This machine is ${s.tailscale_name} on your tailnet. Serve uses your private tailnet only, never Funnel.` : s.tailscale_error}>
          <Button
            disabled={s.tailscale_name === undefined || switching}
            onClick={() =>
              serve({ tailscale: `https://${s.tailscale_name}`, enable_serve_https: true }, (r) => toast(r.serve_error ?? (r.serve || "Tailscale Serve enabled")))
            }
          >
            {s.network.tailscale !== undefined && s.network.tailscale !== "" && !s.network.tailscale.startsWith("http://") ? "Serving" : "Serve over HTTPS"}
          </Button>
          <Button
            disabled={s.tailscale_name === undefined || switching}
            onClick={() =>
              serve({ tailscale: `http://${s.tailscale_name}`, enable_serve_http: true }, (r) => toast(r.serve_http_error ?? `Tailscale Serve (HTTP) enabled. ${HTTP_WARNING}`))
            }
          >
            {s.network.tailscale !== undefined && s.network.tailscale.startsWith("http://") ? "Serving (HTTP)" : "Serve over HTTP"}
          </Button>
        </Row>
        {s.network.tailscale?.startsWith("http://") === true && <p className="text-label text-faint pb-3">{HTTP_WARNING}</p>}
      </Card>
      <TailscaleChecklist rev={n} onChanged={() => setN(n + 1)} />
    </div>
  );
}

/** The Muse app: the devices paired to answer waiting sessions. */
function MuseApp(): ReactElement {
  const [n, setN] = useState(0);
  return (
    <div>
      <H title="Muse app">
        Pair a phone running the Muse app; it walks waiting sessions one card at a time. Each device has its own token, revocable here. The phone reaches this machine over your tailnet through Tailscale Serve; the pairing shows the URL to use.
      </H>
      <MuseDevices key={n} onChanged={() => setN(n + 1)} />
    </div>
  );
}

function Nodes(): ReactElement {
  const app = useApp();
  const [keySetup, setKeySetup] = useState<NodeView | null>(null);
  if (desktop?.environment != null) {
    return (
      <div>
        <H title="SSH hosts">SSH hosts are set up from This machine.</H>
        <Card className="p-4 text-label">
          <p className="mb-3">
            You are working on <span className="font-medium">{desktop.environment.name}</span>. Switch to This machine to add hosts, change their login or set up keys.
          </p>
          <Button onClick={() => void desktop?.switchEnvironment(null)}>Switch to This machine</Button>
        </Card>
      </div>
    );
  }
  return (
    <div>
      <H title="SSH hosts">
        Run workers on other machines over your system OpenSSH (ssh config, ProxyJump and host keys apply). Log in with your keys, or with a password that PenguPool can keep in the
        Keychain. Opening a host switches the whole app to it, like a remote window in an editor: its sessions, teams, skills, memories and settings. PenguPool installs or
        updates itself there (macOS or Linux) and reaches it through an SSH port forward; no remote listener is opened. Switch back with the menu at the top left.
      </H>
      <Card className="mb-4 divide-y divide-border">
        {app.domain.nodes.map((nd) => (
          <NodeRow key={nd.id} nd={nd} onKeySetup={() => setKeySetup(nd)} />
        ))}
      </Card>
      <AddHost />
      {keySetup !== null ? <KeySetupSheet nd={keySetup} onClose={() => setKeySetup(null)} /> : null}
    </div>
  );
}

function loginSummary(nd: NodeView): string {
  if (nd.auth === "password") return nd.saved_password === true ? "password, saved in Keychain" : "password, not saved";
  return nd.identity_file !== undefined ? `key ${nd.identity_file.replace(/^\/Users\/[^/]+/, "~")}` : "keys and agent";
}

function NodeRow({ nd, onKeySetup }: { nd: NodeView; onKeySetup: () => void }): ReactElement {
  const app = useApp();
  const [unlock, setUnlock] = useState(false);
  if (nd.kind === "local") {
    return (
      <div className="flex items-center gap-3 p-3 text-label">
        <Badge tone="success">{nd.status}</Badge>
        <div className="min-w-0 flex-1">
          <p className="font-medium">This machine (hub)</p>
          <p className="truncate text-caption text-muted-foreground">{nd.id}</p>
        </div>
      </div>
    );
  }
  const tone = nd.status === "up" ? "success" : nd.status === "down" ? "danger" : "warning";
  return (
    <div className="p-3 text-label">
      <div className="flex flex-wrap items-center gap-3">
        <Badge tone={tone}>{nd.status === "locked" ? "needs password" : nd.status === "not_installed" ? "not set up" : nd.status}</Badge>
        <div className="min-w-0 flex-1">
          <p className="font-medium">{nd.name}</p>
          <p className="truncate text-caption text-muted-foreground">
            <span className="font-mono">{nd.address}</span> · {loginSummary(nd)}
            {nd.reason !== undefined && nd.status !== "up" ? ` · ${nd.reason}` : ""}
            {nd.status === "not_installed" ? " · PenguPool is installed there when you open it" : ""}
            {nd.outdated === true ? ` · runs ${nd.version ?? "another version"}; updated when you open it` : ""}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {nd.status !== "up" || nd.auth === "password" ? (
            <Button size="sm" variant={nd.status === "locked" ? "default" : "secondary"} onClick={() => setUnlock(!unlock)}>
              <Lock />
              {nd.auth === "password" ? "Connect" : "Use password"}
            </Button>
          ) : null}
          <OpenButton nd={nd} />
          {nd.auth === "password" && nd.status === "up" ? (
            <Button size="sm" onClick={onKeySetup}>
              <KeyRound />
              Set up key login
            </Button>
          ) : null}
          {nd.saved_password === true ? (
            <Button size="sm" variant="ghost" onClick={() => void app.call("node.forget_password", { id: nd.id })}>
              Forget password
            </Button>
          ) : null}
          <Button size="sm" onClick={() => void app.call("node.check", { id: nd.id })}>
            Check
          </Button>
          <Button size="sm" variant="danger" onClick={() => void app.call("node.remove", { id: nd.id })}>
            Remove
          </Button>
        </div>
      </div>
      {unlock ? <UnlockForm nd={nd} onDone={() => setUnlock(false)} /> : null}
    </div>
  );
}

function OpenButton({ nd }: { nd: NodeView }): ReactElement {
  return (
    <Button size="sm" variant="default" disabled={desktop === undefined || nd.status === "locked"} onClick={() => void desktop?.switchEnvironment(nd.id, nd.name)}>
      <MonitorUp />
      Open
    </Button>
  );
}

function UnlockForm({ nd, onDone }: { nd: NodeView; onDone: () => void }): ReactElement {
  const app = useApp();
  const [pw, setPw] = useState("");
  const [save, setSave] = useState(nd.saved_password === true);
  const [busy, setBusy] = useState(false);
  const connect = async (): Promise<void> => {
    setBusy(true);
    const r = await app.call<NodeView>("node.unlock", { id: nd.id, password: pw, save });
    setBusy(false);
    if (r !== undefined) {
      setPw("");
      onDone();
    }
  };
  return (
    <form
      className="mt-3 flex flex-wrap items-center gap-3 rounded-md bg-raised p-3"
      onSubmit={(e) => {
        e.preventDefault();
        void connect();
      }}
    >
      <Input type="password" autoComplete="off" autoFocus className="w-56" value={pw} onChange={(e) => setPw(e.target.value)} placeholder={`Password for ${nd.address}`} />
      <LabeledSwitch checked={save} onCheckedChange={setSave} label="Save in Keychain" />
      <span className="text-caption text-muted-foreground">{save ? "Reconnects on its own after restarts." : "Kept in memory; asked again after the host restarts."}</span>
      <Button type="submit" variant="default" size="sm" disabled={busy || pw === ""} className="ml-auto">
        {busy ? "Connecting…" : "Connect"}
      </Button>
    </form>
  );
}

function AddHost(): ReactElement {
  const app = useApp();
  const [name, setName] = useState("");
  const [addr, setAddr] = useState("");
  const [auth, setAuth] = useState<"key" | "password">("password");
  const [pw, setPw] = useState("");
  const [save, setSave] = useState(true);
  const [trust, setTrust] = useState(true);
  const [busy, setBusy] = useState(false);
  const ready = addr !== "" && (auth === "key" || pw !== "");
  return (
    <Card className="p-4">
      <p className="mb-3 text-label font-medium">Add a host</p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_1.5fr]">
        <Label>
          Name
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="build-box" />
        </Label>
        <Label>
          SSH destination
          <Input value={addr} onChange={(e) => setAddr(e.target.value)} placeholder="user@host or a Host alias" className="font-mono" />
        </Label>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <span className="text-label text-muted-foreground">Log in with</span>
        <div className="inline-flex rounded-md border border-border p-0.5" role="radiogroup" aria-label="Log in with">
          {(
            [
              ["password", "Password"],
              ["key", "SSH key or agent"],
            ] as const
          ).map(([v, label]) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={auth === v}
              className={cn("rounded px-2.5 py-1 text-label", auth === v ? "bg-primary text-primary-foreground" : "hover:bg-hover")}
              onClick={() => setAuth(v)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      {auth === "password" ? (
        <div className="mt-3 space-y-3">
          <Input type="password" autoComplete="off" className="w-full sm:w-72" value={pw} onChange={(e) => setPw(e.target.value)} placeholder="Password" />
          <div className="flex flex-col gap-2">
            <LabeledSwitch checked={save} onCheckedChange={setSave} label="Save the password in the Keychain" />
            <p className="pl-11 text-caption text-muted-foreground">
              {save ? "The host reconnects on its own after restarts and network drops." : "Kept in memory only: after the host restarts it waits for you to enter the password again."}
            </p>
            <LabeledSwitch checked={trust} onCheckedChange={setTrust} label="Trust the host's key on first connect" />
            <p className="pl-11 text-caption text-muted-foreground">Accepts the key of a host this machine has never connected to. A changed key is always refused.</p>
          </div>
          <p className="text-caption text-muted-foreground">You can switch to key login once the host is added: PenguPool installs and checks the key for you.</p>
        </div>
      ) : (
        <p className="mt-3 text-caption text-muted-foreground">Uses your keys, agent and ~/.ssh/config exactly as ssh would. The host must already accept your key.</p>
      )}
      <div className="mt-4 flex justify-end">
        <Button
          variant="default"
          disabled={busy || !ready}
          onClick={async () => {
            setBusy(true);
            const r = await app.call("node.add", { name, address: addr, auth, password: auth === "password" ? pw : undefined, save, trust_host_key: trust });
            setBusy(false);
            if (r !== undefined) {
              setName("");
              setAddr("");
              setPw("");
            }
          }}
        >
          {busy ? "Connecting…" : "Add host"}
        </Button>
      </div>
    </Card>
  );
}

interface SSHKey {
  path: string;
  type: string;
  comment?: string;
  fingerprint?: string;
}

interface KeyStep {
  name: string;
  ok: boolean;
  detail: string;
}

const KEY_STEPS: Array<[string, string]> = [
  ["Key", "Create or pick the key pair on this machine"],
  ["Install", "Add the public key to ~/.ssh/authorized_keys on the host, over your password connection"],
  ["Verify", "Log in with the key alone to prove it works"],
  ["Switch", "Use the key from now on and drop the saved password"],
];

function KeySetupSheet({ nd, onClose }: { nd: NodeView; onClose: () => void }): ReactElement {
  const app = useApp();
  const [keys, setKeys] = useState<SSHKey[] | null>(null);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [steps, setSteps] = useState<KeyStep[] | null>(null);
  useEffect(() => {
    void app.call<SSHKey[]>("node.keys").then((r) => setKeys(r ?? []));
  }, []);
  const ownKey = keys?.find((k) => k.comment?.startsWith("pengupool@") === true && k.path.includes("/.pengupool-ai/"));
  const userKeys = keys?.filter((k) => k !== ownKey) ?? [];
  const done = steps !== null && steps.length === KEY_STEPS.length && steps.every((s) => s.ok);
  const run = async (): Promise<void> => {
    setBusy(true);
    setSteps(null);
    const r = await app.call<{ steps: KeyStep[] }>("node.key_setup", { id: nd.id, key });
    setBusy(false);
    setSteps(r?.steps ?? []);
  };
  return (
    <Sheet
      title={`Set up key login for ${nd.name}`}
      description="PenguPool does every step for you, then checks that the key works before it stops using the password."
      onClose={onClose}
      footer={
        done ? (
          <Button variant="default" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="default" disabled={busy || keys === null} onClick={() => void run()}>
              {busy ? "Setting up…" : steps !== null ? "Try again" : "Set up key login"}
            </Button>
          </>
        )
      }
    >
      <p className="mb-2 text-label font-medium">Key</p>
      <div className="mb-4 space-y-1" role="radiogroup" aria-label="Key">
        <KeyChoice checked={key === ""} onSelect={() => setKey("")} title={ownKey !== undefined ? "PenguPool's key (recommended)" : "Create a PenguPool key (recommended)"}>
          {ownKey !== undefined ? `${ownKey.type} · ${ownKey.fingerprint ?? ""}` : "A new ed25519 key without a passphrase, used only by PenguPool, so the host can reconnect unattended."}
        </KeyChoice>
        {userKeys.map((k) => (
          <KeyChoice key={k.path} checked={key === k.path} onSelect={() => setKey(k.path)} title={k.path.replace(/^\/Users\/[^/]+/, "~")}>
            {k.type}
            {k.comment !== undefined ? ` · ${k.comment}` : ""}
            {k.fingerprint !== undefined ? ` · ${k.fingerprint}` : ""}. Needs to be in ssh-agent if it has a passphrase.
          </KeyChoice>
        ))}
      </div>
      <p className="mb-2 text-label font-medium">Steps</p>
      <ol className="space-y-2">
        {KEY_STEPS.map(([name, what], i) => {
          const s = steps?.find((x) => x.name === name);
          const reached = steps !== null && i < steps.length;
          return (
            <li key={name} className="flex gap-3 text-label">
              <span
                className={cn(
                  "mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border text-caption",
                  s?.ok === true && "border-success bg-success/15 text-success",
                  s?.ok === false && "border-danger bg-danger/15 text-danger",
                  s === undefined && "border-border text-muted-foreground",
                )}
              >
                {s?.ok === true ? <Check className="size-3" /> : s?.ok === false ? <X className="size-3" /> : busy ? <Loader2 className="size-3 animate-spin" /> : i + 1}
              </span>
              <div className="min-w-0">
                <p className={cn(steps !== null && !reached && "text-faint")}>{what}</p>
                {s !== undefined ? <p className={cn("break-words text-caption", s.ok ? "text-muted-foreground" : "text-danger")}>{s.detail}</p> : null}
              </div>
            </li>
          );
        })}
      </ol>
      {steps !== null && !done ? (
        <p className="mt-4 rounded-md bg-warning/10 p-3 text-caption text-warning">The host still logs in with its password. Fix the step above and try again; finished steps are safe to repeat.</p>
      ) : null}
      {done ? <p className="mt-4 rounded-md bg-success/10 p-3 text-caption text-success">{nd.name} now logs in with the key. No password is kept for it.</p> : null}
    </Sheet>
  );
}

function LabeledSwitch({ checked, onCheckedChange, label }: { checked: boolean; onCheckedChange: (v: boolean) => void; label: string }): ReactElement {
  return (
    <span className="inline-flex items-center gap-2 text-label">
      <Switch checked={checked} onCheckedChange={onCheckedChange} label={label} />
      <span aria-hidden>{label}</span>
    </span>
  );
}

function KeyChoice({ checked, onSelect, title, children }: { checked: boolean; onSelect: () => void; title: string; children: ReactNode }): ReactElement {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      onClick={onSelect}
      className={cn("flex w-full items-start gap-3 rounded-md border p-2.5 text-left", checked ? "border-primary bg-primary/5" : "border-border hover:bg-hover")}
    >
      <span className={cn("mt-1 size-3 shrink-0 rounded-full border", checked ? "border-primary bg-primary" : "border-input")} />
      <span className="min-w-0">
        <span className="block break-all text-label font-medium">{title}</span>
        <span className="block break-words text-caption text-muted-foreground">{children}</span>
      </span>
    </button>
  );
}

const CLAUDE_INSTALL = "curl -fsSL https://claude.ai/install.sh | bash";

/** The host: its data, its command-line tool, a restart, updates; how far terminals scroll back; the daily backup. */
function General(): ReactElement {
  const s = useSettings(0);
  return (
    <div>
      <H title="General" />
      {s !== null ? (
        <Card className="mb-4 px-4">
          <Row title="Data folder" hint={s.store}>
            <Button size="sm" disabled={localDesktop === undefined} onClick={() => localDesktop?.revealPath(s.store)}>
              Reveal
            </Button>
          </Row>
          <Row title="Command-line tool" hint={s.executable}>
            <span />
          </Row>
          <Row title="Host" hint={`Listening on ${s.bound ?? "?"}. Restarting keeps every session running.`}>
            <Button size="sm" disabled={localDesktop === undefined} onClick={() => void localDesktop?.restartHost()}>
              Restart host
            </Button>
          </Row>
          <About version={s.version} />
        </Card>
      ) : null}
      <WelcomeSettings />
      <TerminalHistory />
      <DailyBackup />
    </div>
  );
}

/** Which agents are installed here, and whether Claude Code (Ollama) can start. New session offers exactly these. */
function Agents(): ReactElement {
  const app = useApp();
  const [r, setR] = useState<Found | null>(null);
  const [failed, setFailed] = useState(false); // the check itself failed: nothing is known
  const [n, setN] = useState(0);
  useEffect(() => {
    setR(null);
    setFailed(false);
    void app.call<Found>("harness.probe").then((x) => {
      setR(x ?? null);
      setFailed(x === undefined);
    });
  }, [n]);
  const claude = r?.claude;
  // Claude Code (Ollama): ready, or why not (no ollama CLI, no Claude Code, Ollama down or no model).
  const ollama = r === null ? undefined : (r.ollama ?? { version: "", error: "" });
  const ollamaHint =
    ollama === undefined
      ? undefined
      : ollama.version === "" && ollama.error !== undefined
        ? "Claude Code on a local or cloud model, through ollama launch claude. Needs the ollama CLI (ollama.com/download)."
        : claude?.error !== undefined
          ? "Needs Claude Code."
          : (ollama.error ?? `Claude Code through ollama launch claude, on ${ollama.model ?? "Ollama's default model"} (the model Ollama picks for Claude; change it with ollama launch claude).`);
  const ollamaBadge = ollama === undefined ? "" : ollama.version === "" && ollama.error !== undefined ? "not installed" : claude?.error !== undefined || ollama.error !== undefined ? "not ready" : "ready";
  const status = (badge: string, ok: boolean): ReactElement =>
    failed ? (
      <Badge tone="neutral">unknown</Badge>
    ) : badge === "" ? (
      <Loader2 className="size-3.5 animate-spin text-faint" />
    ) : (
      <Badge tone={ok ? "success" : "neutral"}>{badge}</Badge>
    );
  return (
    <div>
      <H title="Agents">
        PenguPool runs the coding agents installed on this machine, each with its own settings for the session. It changes an agent&apos;s own configuration only to add the rules under Agent
        permissions, when you allow them. New session offers the installed ones.
      </H>
      <Card className="mb-4 px-4">
        {failed ? <p className="border-b border-border py-3 text-caption text-danger">Couldn&apos;t check which agents are installed. Check again, or start a session: New session offers them all.</p> : null}
        {AGENTS.filter((a) => !a.hidden).map((a) => {
          const v = r?.[a.id];
          return (
            <Row
              key={a.id}
              title={a.label}
              hint={failed ? "Not checked." : v === undefined ? "Checking…" : (v.error ?? (a.id === "claude" ? `${v.version}. It asks you to sign in to your Anthropic account in its first session.` : v.version))}
            >
              {status(v === undefined ? "" : v.error === undefined ? "installed" : "not installed", v?.error === undefined)}
            </Row>
          );
        })}
        <Row title="Claude Code (Ollama)" hint={failed ? "Not checked." : (ollamaHint ?? "Checking…")}>
          {status(ollamaBadge, ollamaBadge === "ready")}
        </Row>
        <div className="flex items-center gap-2 py-3">
          {claude !== undefined && claude.error !== undefined ? (
            <span className="min-w-0 flex-1 space-y-1 text-caption text-muted-foreground">
              <span className="block">Claude Code installs from a terminal with Anthropic&apos;s installer:</span>
              <Cmd text={CLAUDE_INSTALL} />
            </span>
          ) : (
            <span className="flex-1" />
          )}
          <Button size="sm" onClick={() => setN(n + 1)}>
            <RefreshCw /> Check again
          </Button>
          <Button size="sm" variant="default" onClick={() => app.showDialog(<NewSessionDialog onClose={() => app.showDialog(null)} />)}>
            New session
          </Button>
        </div>
      </Card>
      <AgentPermissions />
    </div>
  );
}

interface AgentPermsView {
  choice: "" | "allowed" | "off";
  agents: Record<string, { on: boolean; file: string; lines?: string[]; changed?: string[]; added?: string[]; error?: string }>;
  commands: string[];
  /** The executable as the rules name it, shell-quoted. */
  cli: string;
}

/** Agent permissions: the owner's one consent to PenguPool's team-command rules in each agent's own settings. */
function AgentPermissions(): ReactElement | null {
  const app = useApp();
  const [v, setV] = useState<AgentPermsView | null>(null);
  const [busy, setBusy] = useState(false);
  const [show, setShow] = useState(false);
  const load = (): void => void app.call<{ agent_permissions: AgentPermsView }>("settings.get").then((r) => r !== undefined && setV(r.agent_permissions));
  useEffect(load, []);
  if (v === null) return null;
  const allowed = v.choice === "allowed";
  const written = (id: string, label: string): ReactElement => {
    const a = v.agents[id];
    const n = v.commands.length;
    if (a === undefined) return <Row title={label}>{null}</Row>;
    const [hint, badge, tone] = !allowed
      ? [`${n} rules in ${a.file}, once you allow them.`, "when on", "neutral" as const]
      : a.error !== undefined
        ? [a.error, "not written", "danger" as const]
        : (a.changed ?? []).length > 0
          ? [`${a.changed?.length ?? 0} of PenguPool's lines were removed from ${a.file} outside PenguPool, so those commands ask again. Turn this off and on to write them again.`, "changed", "warning" as const]
          : (a.added ?? []).length > 0
            ? [`${a.added?.length ?? 0} commands were added to the list since you turned this on, and still ask. Turn this off and on to include them.`, "new commands", "warning" as const]
          : [`${n} rules in ${a.file}${id === "claude" ? " (permissions.allow)" : ""}.`, "global", "success" as const];
    return (
      <Row title={label} hint={hint}>
        <Badge tone={tone}>{badge}</Badge>
      </Row>
    );
  };
  const writtenFiles = Object.values(v.agents).filter((a) => a.on && (a.lines ?? []).length > 0);
  return (
    <div>
      <h3 className="mb-2 text-label font-semibold">Agent permissions</h3>
      <p className="mb-2 text-label text-muted-foreground">
        Agents in a team message each other, ask you questions and look up past work with PenguPool&apos;s command-line tool. Each call can stop on the agent&apos;s own permission prompt. You can let
        agents run the commands below without that prompt. PenguPool still checks every call: who may message whom, that only your own prompt answers a question, that a team change waits
        for your approval, and that a Compass line comes only from the session PenguPool asked.
      </p>
      <Card className="px-4">
        <Row
          title="Let agents run PenguPool's team commands without asking"
          hint={allowed ? "On. Written to each agent's settings; you won't be asked again." : v.choice === "off" ? "Off: agents ask each time." : "Not decided yet: agents ask each time."}
        >
          <Switch
            checked={allowed}
            disabled={busy}
            label="Let agents run PenguPool's team commands without asking"
            onCheckedChange={async (on) => {
              setBusy(true);
              const r = await app.call<{ agent_permissions?: Record<string, { ok: boolean; error?: string }> }>("settings.save", { agent_permissions: on ? "allowed" : "off" });
              setBusy(false);
              load();
              if (r === undefined) return;
              const failed = Object.entries(r.agent_permissions ?? {}).filter(([, x]) => !x.ok);
              if (failed.length > 0) toast.error(failed.map(([a, x]) => `${AGENTS.find((g) => g.id === a)?.label ?? a}: ${x.error ?? "not written"}`).join(" "));
              else toast.success(on ? "Agents may run PenguPool's team commands without asking." : "Agents will ask again.");
            }}
          />
        </Row>
        <Row
          title="Commands"
          hint={
            <>
              {v.commands.map((c) => (
                <span key={c} className="block font-mono text-caption text-foreground [overflow-wrap:anywhere]">
                  {c}
                </span>
              ))}
              <span className="block">Only these, with any message or search text. The list is fixed.</span>
              <span className="block [overflow-wrap:anywhere]">
                The rules name PenguPool by its full path: <span className="font-mono">{v.cli}</span>
              </span>
            </>
          }
        >
          {null}
        </Row>
        <Row title="Never included" hint="Push, merge, release, delete, and every other command still ask, as they do today. So does a command that chains one of these with anything else.">
          <Lock className="size-3.5 text-muted-foreground" aria-hidden />
        </Row>
        {written("claude", "Claude Code")}
        {written("codex", "Codex")}
        <Row title="OMP" hint="Only when its approval mode asks; its default (yolo) never asks. PenguPool writes no rules for it yet.">
          <Badge tone="neutral">not yet</Badge>
        </Row>
        <Row title="pi" hint="Never asks, so nothing is needed.">
          <Badge tone="neutral">not needed</Badge>
        </Row>
        <Row title="Muse Code" hint="Not supported yet: Muse has no documented way to add a rule. It still asks.">
          <Badge tone="neutral">not yet</Badge>
        </Row>
        {writtenFiles.length > 0 ? (
          <div className="border-t border-border py-3">
            <button type="button" className="text-caption text-muted-foreground underline" aria-expanded={show} onClick={() => setShow(!show)}>
              {show ? "Hide what is written" : "Show what is written"}
            </button>
            {show ? (
              <div className="mt-2 space-y-2 rounded-md bg-muted p-2 font-mono text-caption">
                {writtenFiles.map((a) => (
                  <div key={a.file}>
                    <span className="block font-semibold [overflow-wrap:anywhere]">{a.file}</span>
                    {(a.lines ?? []).map((l) => (
                      <span key={l} className="block [overflow-wrap:anywhere]">
                        {l}
                      </span>
                    ))}
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        ) : null}
      </Card>
      <p className="mt-2 text-caption text-faint">
        PenguPool adds only the lines shown above to each agent&apos;s own settings, and removes exactly those when you turn this off. They apply to every session of that agent, including ones you start
        outside PenguPool. A session already running may need a restart to pick them up.
      </p>
    </div>
  );
}

/** A command to run in a terminal, with a copy button. */
function Cmd({ text }: { text: string }): ReactElement {
  return (
    <span className="flex items-center gap-2 rounded-md bg-muted px-2 py-1.5 font-mono text-caption text-foreground">
      <span className="min-w-0 flex-1 break-all">{text}</span>
      <Button
        size="sm"
        variant="ghost"
        onClick={() =>
          void navigator.clipboard.writeText(text).then(
            () => toast.success("Copied"),
            () => toast.error("Could not copy"),
          )
        }
      >
        Copy
      </Button>
    </span>
  );
}

/** The running version, and a check for a newer release (update.check). */
function About({ version }: { version?: string }): ReactElement {
  const app = useApp();
  const [u, setU] = useState<Update | null>(null);
  const [busy, setBusy] = useState(false);
  const check = async (): Promise<void> => {
    setBusy(true);
    setU((await app.call<Update>("update.check", { refresh: true })) ?? null);
    setBusy(false);
  };
  const result =
    u === null ? "" : u.error !== undefined ? u.error : u.newer ? `PenguPool ${u.latest ?? ""} is available.` : "This is the newest version.";
  return (
    <Row title="About PenguPool" hint={`Version ${version ?? "?"}${result !== "" ? `. ${result}` : ""}`}>
      {u?.newer === true ? (
        <Button size="sm" variant="default" onClick={() => app.showDialog(<UpdateSheet u={u} onClose={() => app.showDialog(null)} />)}>
          Update…
        </Button>
      ) : (
        <Button size="sm" disabled={busy} onClick={() => void check()}>
          {busy ? <Loader2 className="animate-spin" /> : null} Check for updates
        </Button>
      )}
    </Row>
  );
}

interface SyncView {
  github: boolean;
  interval_min: number;
  repo?: string;
  /** Sync is on but no repository is picked: nothing syncs, and nothing is created, until the owner picks one. */
  needs_repo?: boolean;
  /** A name to suggest (a new repository in the account); used only when the owner saves it. */
  suggested_repo?: string;
  local_dir: string;
  status: { last_snapshot?: string; last_push?: string; last_checked?: string; last_error?: string; running: boolean; compact_suggested?: boolean; compact_reason?: string };
}

/** The daily snapshot on this machine of the Compass, helper experience and the team records. Always on. */
function DailyBackup(): ReactElement {
  const app = useApp();
  const [b, setB] = useState<SyncView | null>(null);
  useEffect(() => {
    void app.call<SyncView>("sync.get").then((r) => setB(r ?? null));
  }, []);
  if (b === null) return <></>;
  return (
    <Card className="mb-4 px-4">
      <Row title="Daily backup" hint={`The Compass, helpers' experience and team records: the last 14 days, in ${b.local_dir}. Always on.`}>
        <span className="text-label text-muted-foreground">{b.status.last_snapshot !== undefined ? shortTime(b.status.last_snapshot) : "Not yet"}</span>
      </Row>
    </Card>
  );
}

/** GitHub: the account PenguPool uses, and the sync of the record through a private repository. */
function GitHub(): ReactElement {
  return (
    <div>
      <H title="GitHub" />
      <div className="mb-4">
        <GitHubAccount />
      </div>
      <Sync />
    </div>
  );
}

/** Sync with GitHub: the Compass, helper experience and the team records, through a private repository. */
function Sync(): ReactElement {
  const app = useApp();
  const [b, setB] = useState<SyncView | null>(null);
  const [busy, setBusy] = useState(false);
  const [repo, setRepo] = useState<string>("");
  const [repoError, setRepoError] = useState("");
  const [repos, setRepos] = useState<{ full_name: string; private: boolean; description: string }[] | null>(null);
  useEffect(() => {
    void app.call<SyncView>("sync.get").then((r) => r !== undefined && setRepo(r.repo ?? ""));
  }, []);
  useEffect(() => {
    const load = (): void => void app.call<SyncView>("sync.get").then((r) => r !== undefined && setB(r));
    load();
    const t = window.setInterval(load, 5000);
    return () => window.clearInterval(t);
  }, []);
  if (b === null) return <p className="text-label text-faint">Loading…</p>;
  const st = b.status;
  return (
    <div>
      <p className="mb-2 text-label text-muted-foreground">
        The Compass and helpers&apos; experience: with sync on, PenguPool syncs the record through a private repository you pick, one your account can push to or a new name it creates on the first sync. Nothing syncs until you pick one.
      </p>
      <Card className="px-4">
        <Row
          title="Sync with GitHub"
          hint={
            b.repo !== undefined && b.repo !== ""
              ? `To github.com/${b.repo} (private), only when something changed.`
              : b.needs_repo === true
                ? "Choose a repository below to start: nothing syncs, and no repository is created, until you save one."
                : "Needs the GitHub account above, and a repository you choose below."
          }
        >
          <Switch
            checked={b.github}
            label="Sync with GitHub"
            onCheckedChange={async (v) => {
              const r = await app.call<SyncView>("sync.set", { github: v });
              if (r !== undefined) setB(r);
            }}
          />
        </Row>
        <Row
          title="Repository"
          hint={
            <>
              Any private repository your GitHub account can push to, or a new name to create one. Every machine and account that syncs with the same repository shares one
              file, pengupool-agents.db (SQLite, merged row by row): the Compass, experience and team records written on one reach the others. PenguPool writes nothing else there but a README in a repository it creates.
              {b.needs_repo === true ? (
                <span className="block font-semibold text-foreground">
                  Choose a repository to start.
                  {b.suggested_repo !== undefined && repo.trim() === "" ? (
                    <>
                      {" "}
                      <button type="button" className="underline" onClick={() => setRepo(b.suggested_repo ?? "")}>
                        Use {b.suggested_repo}
                      </button>{" "}
                      (a new private repository, created when you save it).
                    </>
                  ) : null}
                </span>
              ) : null}
              {repoError !== "" ? <span className="block text-danger">{repoError}</span> : null}
            </>
          }
        >
          <form
            className="flex gap-2"
            onSubmit={async (e) => {
              e.preventDefault();
              setRepoError("");
              try {
                setB(await app.conn.request<SyncView>("sync.set", { repo: repo.trim() }));
                toast.success("Saved");
              } catch (err) {
                setRepoError((err as Error).message);
              }
            }}
          >
            <Input
              className="w-56 font-mono"
              value={repo}
              list="sync-repos"
              onFocus={() => {
                // Not cached on failure: the next focus tries again (after signing in, say).
                if (repos === null)
                  void app.conn.request<{ full_name: string; private: boolean; description: string }[]>("sync.repos").then(
                    (r) => {
                      setRepos(r);
                      setRepoError("");
                    },
                    (err: Error) => setRepoError(err.message),
                  );
              }}
              onChange={(e) => setRepo(e.target.value)}
              placeholder={b.suggested_repo ?? "owner/name"}
              aria-label="Sync repository"
            />
            <datalist id="sync-repos">
              {(repos ?? []).map((r) => (
                <option key={r.full_name} value={r.full_name}>
                  {r.description}
                </option>
              ))}
            </datalist>
            <Button type="submit" disabled={repo.trim() === (b.repo ?? "")}>
              Save
            </Button>
          </form>
        </Row>
        <Row title="Every" hint="Minutes between pushes, 1 to 60. PenguPool also backs up when it starts.">
          <Select
            value={String(b.interval_min)}
            onChange={async (e) => {
              const r = await app.call<SyncView>("sync.set", { interval_min: Number(e.target.value) });
              if (r !== undefined) setB(r);
            }}
          >
            {[1, 5, 15, 30, 60].map((m) => (
              <option key={m} value={m}>
                {m} min
              </option>
            ))}
          </Select>
        </Row>
        <Row
          title="Last sync"
          hint={
            b.needs_repo === true ? (
              "Choose a repository to start."
            ) : st.last_error !== undefined ? (
              <span className="text-danger">Failed: {st.last_error}</span>
            ) : st.last_push !== undefined ? (
              `Pushed ${shortTime(st.last_push)}; last checked ${shortTime(st.last_checked)}.`
            ) : (
              "Nothing pushed yet."
            )
          }
        >
          <Button
            disabled={busy || st.running}
            onClick={async () => {
              setBusy(true);
              const r = await app.call<SyncView>("sync.now");
              setBusy(false);
              if (r !== undefined) {
                setB(r);
                toast.success("Backed up");
              }
            }}
          >
            {busy || st.running ? <Loader2 className="animate-spin" /> : <RefreshCw />} Back up now
          </Button>
        </Row>
        {st.compact_suggested === true ? (
          <Row
            title="Sync history is large: compact it"
            hint={`${st.compact_reason ?? ""}. Keeps the last 30 days of commits and replaces the rest with one base commit; other machines carry on as they are.`}
          >
            <Button
              disabled={busy || st.running}
              onClick={async () => {
                if (!window.confirm("Rewrite the sync repository's history? The commits older than 30 days are replaced by one base commit.")) return;
                setBusy(true);
                const r = await app.call<SyncView>("sync.compact");
                setBusy(false);
                if (r !== undefined) {
                  setB(r);
                  toast.success("History compacted");
                }
              }}
            >
              Compact history
            </Button>
          </Row>
        ) : null}
      </Card>
    </div>
  );
}

type MuseDevice = { name: string; created_at: string; last_seen?: string };

/** Pair Muse devices, one at a time, each with its own revocable token. */
function MuseDevices({ onChanged }: { onChanged: () => void }): ReactElement {
  const app = useApp();
  const [devices, setDevices] = useState<MuseDevice[] | null>(null);
  const [code, setCode] = useState<{ code: string; url: string } | null>(null);
  useEffect(() => {
    void app.call<MuseDevice[]>("muse.devices").then((d) => setDevices(d ?? []));
  }, [app]);
  return (
    <Card className="mb-4 px-4">
      <div className="border-b border-border py-3">
        <p className="text-label font-medium">Muse devices</p>
        <p className="text-caption text-muted-foreground">Pair the Muse app on each device. Each device gets its own token; revoke one any time.</p>
      </div>
      {(devices ?? []).map((d) => (
        <Row key={d.name} title={d.name} hint={d.last_seen !== undefined ? `Last seen ${d.last_seen}` : `Paired ${d.created_at}`}>
          <Button
            size="sm"
            onClick={async () => {
              if ((await app.call("muse.revoke", { name: d.name })) !== undefined) {
                setDevices((prev) => (prev ?? []).filter((x) => x.name !== d.name));
                onChanged();
              }
            }}
          >
            Revoke
          </Button>
        </Row>
      ))}
      <div className="flex flex-wrap items-center gap-2 py-3">
        <Button
          size="sm"
          onClick={async () => {
            const r = await app.call<{ code: string; url: string }>("muse.pairCode", {});
            if (r !== undefined) setCode({ code: r.code, url: r.url });
          }}
        >
          Pair a Muse device
        </Button>
        {code !== null ? (
          <p className="text-caption">
            Enter <span className="font-mono font-semibold">{code.code}</span> in the Muse app within 5 minutes. URL: <span className="font-mono">{code.url}</span>
          </p>
        ) : null}
      </div>
    </Card>
  );
}

type TailscaleCheck = { id: string; label: string; ok: boolean; detail: string; fix?: { kind: "link" | "button"; label: string; url?: string; action?: string } };

function TailscaleChecklist({ rev, onChanged }: { rev: number; onChanged: () => void }): ReactElement {
  const app = useApp();
  const [approveUrl, setApproveUrl] = useState<string | null>(null);
  const [checks, setChecks] = useState<TailscaleCheck[]>([]);
  // Its own request: the checks run the Tailscale CLI, which must not slow every settings read.
  useEffect(() => {
    void app.call<TailscaleCheck[]>("settings.tailscale").then((r) => setChecks(r ?? []));
  }, [rev]);
  if (checks.length === 0) return <></>;
  return (
    <Card className="mb-4 px-4">
      <div className="border-b border-border py-3">
        <p className="text-label font-medium">Tailscale checklist</p>
        <p className="text-caption text-muted-foreground">Each step in order. Serve points at this app over your tailnet only, never Funnel.</p>
      </div>
      {checks.map((c) => (
        <Row key={c.id} title={c.label} hint={c.ok ? c.detail : c.detail}>
          {c.ok ? (
            <Badge tone="success">ok</Badge>
          ) : c.fix === undefined ? null : c.fix.kind === "link" ? (
            <Button size="sm" onClick={() => window.open(c.fix?.url, "_blank", "noopener")}>
              {c.fix.label}
            </Button>
          ) : c.fix.action === "serve" ? (
            <Button
              size="sm"
              onClick={async () => {
                setApproveUrl(null);
                const r = await app.call<{ serve?: string; serve_error?: string; serve_approve_url?: string }>("settings.save", { enable_serve: true });
                if (r === undefined) return;
                if (r.serve_approve_url !== undefined) setApproveUrl(r.serve_approve_url);
                else toast(r.serve_error ?? r.serve ?? "Serve updated");
                onChanged();
              }}
            >
              {c.fix.label}
            </Button>
          ) : (
            <Button size="sm" onClick={() => onChanged()}>
              {c.fix.label}
            </Button>
          )}
        </Row>
      ))}
      {approveUrl !== null ? (
        <div className="py-3">
          <Button size="sm" onClick={() => window.open(approveUrl, "_blank", "noopener")}>
            Approve in Tailscale
          </Button>
        </div>
      ) : null}
    </Card>
  );
}
