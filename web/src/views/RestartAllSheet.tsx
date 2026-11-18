import { useEffect, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { harnessLabel, isAgent, useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Sheet } from "../components/ui/dialog";
import { followInSettings, restartActive, restartPlan, type RestartItem } from "./restartAll";

/** Restart all's confirm sheet: what restarts, what waits for its turn, what a running restart has. */
export function RestartAllSheet({ onStarted }: { onStarted?: (items: RestartItem[]) => void }): ReactElement {
  const app = useApp();
  const [run, setRun] = useState<RestartItem[] | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    const read = (): void => void app.call<RestartItem[]>("run.restartStatus").then((r) => live && r !== undefined && setRun(r));
    read();
    const t = setInterval(read, 2000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [app]);
  const close = (): void => app.showDialog(null);
  const going = run !== null && restartActive(run);
  const inRun = new Set(going ? run.map((it) => it.session_id) : []);
  const plan = restartPlan(app.sessions, isAgent);
  const fresh = plan.filter((e) => !inRun.has(e.s.id));
  const n = fresh.length;
  const waiting = fresh.filter((e) => e.waits).length;
  const start = async (): Promise<void> => {
    setBusy(true);
    const r = await app.call<RestartItem[]>("run.restartAll", {});
    setBusy(false);
    if (r === undefined) return; // the host's toast says why
    close();
    onStarted?.(r);
  };
  return (
    <Sheet
      title="Restart all sessions"
      description="For after you update an agent (Claude Code, Codex, pi or OMP): each session restarts on the new version and resumes its conversation, in its team and folder."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Not now
          </Button>
          <Button variant="default" disabled={busy || run === null || n === 0} onClick={() => void start()}>
            {busy ? "Starting…" : n === 0 ? "Restart sessions" : `Restart ${n} session${n === 1 ? "" : "s"}`}
          </Button>
        </>
      }
    >
      {plan.length === 0 ? (
        <p className="text-label text-muted-foreground">No agent session is running, so there is nothing to restart.</p>
      ) : (
        <div className="flex flex-col gap-3">
          {going ? (
            <p className="text-label text-muted-foreground">
              A restart is already running; Settings → Agents follows it.{" "}
              {n === 0 ? "Every running session is in it, so there is nothing to add." : n === 1 ? "Restarting now adds only the session not in it yet." : `Restarting now adds only the ${n} sessions not in it yet.`}
            </p>
          ) : null}
          {waiting > 0 ? (
            <p className="text-label text-muted-foreground">
              {waiting === 1 ? "One session is mid-turn or waiting on you: it restarts" : `${waiting} sessions are mid-turn or waiting on you: they restart`} once the turn ends, never cut off. You can cancel a waiting one from Settings → Agents.
            </p>
          ) : null}
          <ul aria-label="Sessions to restart" className="flex flex-col divide-y divide-border rounded-md border border-border">
            {plan.map(({ s, waits }) => (
              <li key={s.id} className="flex min-w-0 items-center gap-2 px-3 py-1.5 text-label">
                <span className="min-w-0 truncate" title={s.name}>
                  {s.name}
                </span>
                <span className="shrink-0 text-muted-foreground">· {harnessLabel(s.harness)}</span>
                <span className={inRun.has(s.id) ? "ml-auto shrink-0 text-faint" : waits ? "ml-auto shrink-0 text-info" : "ml-auto shrink-0 text-muted-foreground"}>
                  {inRun.has(s.id) ? "already in this run" : waits ? "waits for its turn to end" : "restarts in turn"}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Sheet>
  );
}

/** The map's Restart all: the sheet, then a toast that leads to Settings › Agents. */
export function restartAllFromMap(app: ReturnType<typeof useApp>): void {
  app.showDialog(
    <RestartAllSheet
      onStarted={(items) =>
        toast(`Restarting ${items.length} session${items.length === 1 ? "" : "s"}`, {
          description: "Each restarts in turn; busy ones wait for their turn to end.",
          duration: 10_000,
          action: { label: "Follow in Settings", onClick: () => followInSettings(app.setNav) },
        })
      }
    />,
  );
}
