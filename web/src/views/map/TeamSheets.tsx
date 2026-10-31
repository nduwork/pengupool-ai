import { useState, type ReactElement } from "react";
import { toast } from "sonner";

import { ago, useApp } from "../../app/ctx";
import { Button } from "../../components/ui/button";
import { Input, Label, Switch } from "../../components/ui/controls";
import { Sheet } from "../../components/ui/dialog";
import type { Team } from "../../protocol/domain";

// A group's settings and closing live on its frame in the map; there is no Teams page. Tasks come from
// the owner's instructions or a tracker the agents reach themselves.

/** The team's rarely changed settings: budget, and whether paired devices see it. */
export function TeamSettingsSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const t = app.domain.teams.find((x) => x.id === team.id) ?? team; // live, so switches reflect changes
  const [budget, setBudget] = useState(String(t.budget_usd ?? ""));
  return (
    <Sheet title={`Settings of ${t.name}`} onClose={() => app.showDialog(null)}>
      <div className="grid gap-5 text-label">
        <form
          className="grid gap-1.5"
          onSubmit={async (e) => {
            e.preventDefault();
            if ((await app.call("team.budget", { team_id: t.id, budget_usd: Number(budget) || 0 })) !== undefined) toast.success("Budget saved");
          }}
        >
          <Label hint="In USD; 0 clears it.">
            Budget
            <div className="flex gap-2">
              <Input type="number" min={0} step="1" className="w-32" value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="USD" />
              <Button type="submit" size="sm">
                Save
              </Button>
            </div>
          </Label>
        </form>
        <div className="flex items-center justify-between gap-3">
          <span>
            Allow iOS access
            <span className="block text-caption text-muted-foreground">Paired Muse devices can see this team and answer its asks.</span>
          </span>
          <Switch checked={t.ios_access} onCheckedChange={(v) => void app.call("team.policy", { team_id: t.id, ios_access: v })} label="Allow iOS access" />
        </div>
        <div className="flex items-center justify-between gap-3">
          <span>
            Lead decides from your Compass
            <span className="block text-caption text-muted-foreground">When a lead asks, PenguPool may decide a member's waiting ask or check-in from your confident Compass lines; you can call each decision wrong.</span>
          </span>
          <Switch checked={t.no_lead_compass !== true} onCheckedChange={(v) => void app.call("team.lead_compass", { team_id: t.id, on: v })} label="Lead decides from your Compass" />
        </div>
      </div>
    </Sheet>
  );
}

export function CloseTeamSheet({ team }: { team: Team }): ReactElement {
  const app = useApp();
  const close = (): void => app.showDialog(null);
  return (
    <Sheet
      title={`Close ${team.name}?`}
      description="Every helper is closed; the main session keeps running. The team's record stays."
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Keep the team
          </Button>
          <Button
            variant="danger"
            onClick={async () => {
              if ((await app.call("team.close", { team_id: team.id })) !== undefined) close();
            }}
          >
            Close team
          </Button>
        </>
      }
    >
      <span />
    </Sheet>
  );
}

/** Closed teams, each deletable: deleting drops its helpers' team notes and raw turns. */
export function ClosedTeamsSheet(): ReactElement {
  const app = useApp();
  const [busy, setBusy] = useState("");
  const [armed, setArmed] = useState(""); // the first click asks, the second deletes
  const closed = app.domain.teams.filter((t) => t.state !== "active");
  return (
    <Sheet
      title="Closed groups"
      description="Deleting one drops its helpers' team notes and raw turns. Helper agents keep their other experience; repositories and branches are untouched."
      onClose={() => app.showDialog(null)}
    >
      <ul className="grid gap-1 text-label">
        {closed.length === 0 ? <li className="text-muted-foreground">No closed groups.</li> : null}
        {closed.map((t) => (
          <li key={t.id} className="flex items-center gap-3">
            <span className="min-w-0 flex-1 truncate font-medium">{t.name}</span>
            <span className="text-caption text-faint">started {ago(t.created_at)}</span>
            <Button
              size="sm"
              variant="ghost"
              className="text-danger"
              disabled={busy !== ""}
              onClick={async () => {
                if (armed !== t.id) return setArmed(t.id);
                setBusy(t.id);
                await app.call("team.delete", { team_id: t.id });
                setBusy("");
              }}
            >
              {busy === t.id ? "Deleting…" : armed === t.id ? "Delete for good" : "Delete"}
            </Button>
          </li>
        ))}
      </ul>
    </Sheet>
  );
}
