import { Check, Circle, X } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";

import { useApp, usePersisted } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/controls";
import { cn } from "../lib/utils";
import { stepsOf, type Setup } from "./checklist";
import { Mascot } from "./Mascot";
import { NewSessionDialog } from "./NewSession";

/** The first-run checklist: each step is detected from the app's own state and links to where it is done; it hides once all are done or dismissed. */
export function GettingStarted(): ReactElement | null {
  const app = useApp();
  const [dismissed, setDismissed] = usePersisted("gettingStarted.dismissed", false);
  const [, setSection] = usePersisted("settings.section", "general");
  const [found, setFound] = useState<Omit<Setup, "teams"> | null>(null);
  const teams = app.domain.teams.length;
  useEffect(() => {
    if (dismissed || !app.connected) return;
    // A background read: it waits for the connection and logs a failure rather than toasting it.
    const read = <R,>(type: string): Promise<R | undefined> => app.conn.request<R>(type, {}).catch((e) => void console.warn(type, e));
    void Promise.all([
      read<Record<string, { error?: string }>>("harness.probe"),
      read<{ active: string }>("decision.providers"),
    ]).then(([h, d]) => setFound({ agent: h?.claude !== undefined && h.claude.error === undefined, decision: (d?.active ?? "") !== "" }));
  }, [app.nav, app.connected, dismissed]); // refreshed on coming back from the page where a step is done
  if (dismissed || found === null) return null;
  const steps = stepsOf({ ...found, teams });
  if (steps.every((s) => s.done)) return null;
  return (
    <Card className="w-full max-w-sm p-3 text-left text-label">
      <div className="mb-1 flex items-center">
        <span className="flex-1 font-medium">Getting started</span>
        <button type="button" onClick={() => setDismissed(true)} aria-label="Dismiss Getting started" className="rounded p-0.5 text-faint hover:bg-hover">
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
          className={cn("flex h-7 w-full items-center gap-2 rounded-md px-1 hover:bg-hover", s.done && "text-faint line-through")}
        >
          {s.done ? <Check className="size-3.5 text-success" /> : <Circle className="size-3.5 text-faint" />}
          {s.label}
        </button>
      ))}
    </Card>
  );
}

/** The Workspace before the first session: the mascot, one action, and the checklist. */
export function Welcome(): ReactElement {
  const app = useApp();
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-background p-8 text-center">
      <Mascot className="size-20" />
      <p className="text-title font-medium">Welcome to PenguPool AI</p>
      <p className="max-w-md text-label text-muted-foreground">Start a session with an agent in a folder; teams and helpers build on it. Say a rule once in Compass AI and your agents stop asking.</p>
      <Button variant="default" onClick={() => app.showDialog(<NewSessionDialog onClose={() => app.showDialog(null)} />)}>
        New session
      </Button>
      <GettingStarted />
    </div>
  );
}
