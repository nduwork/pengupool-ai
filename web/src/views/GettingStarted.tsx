import { ArrowLeft, Check, Circle, FolderOpen, Users, X } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { AGENTS, useApp, usePersisted } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Card, Kbd } from "../components/ui/controls";
import { cn } from "../lib/utils";
import { stepsOf, type Setup } from "./checklist";
import { Mascot } from "./Mascot";
import { NewSessionDialog } from "./NewSession";
import { StartTeamSheet } from "./StartTeam";

const PROVIDER_LABEL: Record<string, string> = { ollama: "Local hosted", jev: "Online" };

/** The Setup card: what setup is left, each row linking to the shipped control; it hides once all are done or dismissed. */
export function GettingStarted({ reserve = false }: { reserve?: boolean }): ReactElement | null {
  const app = useApp();
  const [dismissed, setDismissed] = usePersisted("gettingStarted.dismissed", false);
  const [, setSection] = usePersisted("settings.section", "general");
  const [found, setFound] = useState<Setup | null>(null);
  useEffect(() => {
    if (dismissed || !app.connected) return;
    // A background read: it waits for the connection and logs a failure rather than toasting it.
    const read = <R,>(type: string): Promise<R | undefined> => app.conn.request<R>(type, {}).catch((e) => void console.warn(type, e));
    void Promise.all([
      read<Record<string, { version: string; error?: string }>>("harness.probe"),
      read<{ active: string; providers: Record<string, { model?: string }> | null }>("decision.providers"),
      read<{ agent_permissions?: { choice: string } }>("settings.get"),
    ]).then(([h, d, s]) => {
      const ready = AGENTS.find((a) => !a.hidden && h?.[a.id] !== undefined && h[a.id].error === undefined);
      const active = d?.active ?? "";
      const model = active === "" ? "" : (d?.providers?.[active]?.model ?? "");
      setFound({
        agent: ready === undefined ? "" : `${ready.label} ${h?.[ready.id].version ?? ""}`.trim(),
        decision: active === "" ? "" : `${PROVIDER_LABEL[active] ?? active}${model !== "" ? ` · ${model}` : ""}`,
        permissions: s?.agent_permissions?.choice ?? "",
      });
    });
  }, [app.nav, app.connected, dismissed]); // refreshed on coming back from the page where a step is done
  const hold = reserve ? <div className="min-h-[124px] w-full" /> : null; // keeps what is under it from jumping
  if (dismissed) return null;
  if (found === null) return hold;
  const steps = stepsOf(found);
  if (steps.every((s) => s.done)) return null;
  return (
    <Card className="w-full p-3 text-left text-label">
      <div className="mb-1 flex items-center gap-2">
        <span className="font-medium">Setup</span>
        <span className="flex-1 text-caption tabular-nums text-faint">
          {steps.filter((s) => s.done).length} of {steps.length} done
        </span>
        <button type="button" onClick={() => setDismissed(true)} aria-label="Dismiss Setup" className="rounded p-0.5 text-faint hover:bg-hover">
          <X className="size-3.5" />
        </button>
      </div>
      {steps.map((s) => (
        <button
          key={s.id}
          type="button"
          onClick={() => {
            if (s.section !== undefined) setSection(s.section);
            app.setNav(s.nav);
          }}
          className="flex h-8 w-full items-center gap-2 rounded-md px-1 hover:bg-hover"
        >
          {s.done ? <Check className="size-3.5 shrink-0 text-success" /> : <Circle className="size-3.5 shrink-0 text-faint" />}
          <span className="shrink-0 font-medium">{s.label}</span>
          <span className={cn("min-w-0 flex-1 truncate text-right text-caption", s.done ? "text-faint" : "text-muted-foreground")} title={s.detail}>
            {s.detail}
          </span>
          {s.done ? null : (
            <span aria-hidden className="shrink-0 rounded-md border border-border px-2 py-0.5 text-caption">
              {s.action}
            </span>
          )}
        </button>
      ))}
    </Card>
  );
}

/** The Workspace before the first session, or when the owner opens it: two ways to start, what setup is left, and whether to show it. */
export function Welcome({ onBack }: { onBack?: () => void }): ReactElement {
  const app = useApp();
  useEffect(() => {
    if (onBack === undefined) return;
    const esc = (e: KeyboardEvent): void => {
      if (e.key === "Escape") onBack();
    };
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [onBack]);
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-background p-8 text-center">
      {onBack !== undefined ? (
        <Button variant="ghost" size="sm" className="absolute left-3 top-3" onClick={onBack}>
          <ArrowLeft /> Back to Workspace
        </Button>
      ) : null}
      <Mascot className="size-16" />
      <p className="text-title font-medium">Welcome to PenguPool</p>
      <p className="max-w-md text-label text-muted-foreground">Start a team led by team-lead-agent with the specialists you pick, or one agent in a folder.</p>
      <div className="grid w-full max-w-md grid-cols-2 gap-2">
        <Button size="lg" variant="default" onClick={() => app.showDialog(<StartTeamSheet onClose={() => app.showDialog(null)} onStarted={onBack} />)}>
          <Users /> Start with a specialist
        </Button>
        <Button size="lg" variant="outline" onClick={() => app.showDialog(<NewSessionDialog onClose={() => app.showDialog(null)} />)}>
          <FolderOpen /> Start in a folder <Kbd>⌘N</Kbd>
        </Button>
      </div>
      <div className="w-full max-w-md">
        <GettingStarted reserve />
      </div>
      <label className="flex items-center gap-2 text-caption text-muted-foreground">
        <input
          type="checkbox"
          checked={app.welcome.show}
          onChange={(e) => {
            const show = e.target.checked;
            app.welcome.setShow(show);
            if (!show) {
              onBack?.();
              toast("Welcome hidden. Reopen it from Settings → General.", { action: { label: "Undo", onClick: () => app.welcome.setShow(true) } });
            }
          }}
        />
        Show Welcome when Workspace is empty
      </label>
    </div>
  );
}
