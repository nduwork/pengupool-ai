import { BookOpen, ChevronsRight, GripVertical } from "lucide-react";
import type { ReactElement } from "react";

import { useApp } from "../../app/ctx";
import { Button } from "../../components/ui/button";
import { Tip } from "../../components/ui/controls";
import { onMap } from "../skillgroups";

// The Helper pool drawer on the map's right edge (v1's skill-repo drawer): only the helpers
// the Skill library manages, never the work repos sessions run in. Drag a skill onto a card to add it
// as a helper; onto empty canvas to start a session in the skill repo. Only installed helpers the
// owner ticked "On the map" on the Helpers page are listed: choosing them is that page's job.

export const DND = { session: "application/x-pp-session", skill: "application/x-pengupool-skill", repo: "application/x-pp-repo" };

export function AgentPool({ open, setOpen, width, setWidth, flash }: { open: boolean; setOpen: (v: boolean) => void; width: number; setWidth: (v: number) => void; flash: (ids: string[]) => void }): ReactElement {
  const app = useApp();
  const skills = onMap(app.domain.skills);
  // A helper's sessions: those it runs as (a helper works in a scratch folder), or any opened in its folder.
  const liveIn = (sk: { id: string; path: string }): string[] =>
    app.sessions.filter((s) => s.state === "running" && (s.skill_id === sk.id || s.cwd === sk.path || s.cwd.startsWith(sk.path + "/"))).map((s) => s.id);

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-expanded={false}
        className="absolute right-0 top-3 z-20 rounded-l-md border border-r-0 border-border bg-raised px-1 py-3 text-caption font-medium text-muted-foreground shadow-sm [writing-mode:vertical-rl] hover:text-foreground"
      >
        Helper pool
      </button>
    );
  }
  const grip = (e: React.PointerEvent): void => {
    const startX = e.clientX;
    const startW = width;
    const move = (ev: PointerEvent): void => setWidth(Math.min(520, Math.max(200, startW + (startX - ev.clientX))));
    const up = (): void => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  return (
    <aside className="relative flex shrink-0 flex-col border-l border-border bg-panel" style={{ width }} aria-label="Helper pool">
      <div
        className="absolute -left-1 bottom-0 top-0 w-2 cursor-col-resize hover:bg-primary/40"
        onPointerDown={grip}
        onDoubleClick={() => setWidth(260)}
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize Helper pool"
      />
      <div className="flex h-9 items-center gap-1 border-b border-border px-2">
        <span className="px-1 text-caption font-medium">Helpers · {skills.length}</span>
        <Button size="icon-sm" variant="ghost" className="ml-auto" onClick={() => setOpen(false)} aria-label="Collapse Helper pool">
          <ChevronsRight />
        </Button>
      </div>
      <p className="px-3 py-2 text-caption text-muted-foreground">
        Your helpers. Drag one onto a card to add it to that team, or onto empty canvas to start it on its own.
      </p>
      <ul className="min-h-0 flex-1 space-y-1 overflow-auto px-2 pb-2">
        {skills.map((sk) => {
          const live = liveIn(sk);
          return (
            <li
              key={sk.id}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData(DND.skill, sk.id);
                e.dataTransfer.effectAllowed = "copy";
              }}
              className="grid grid-cols-1 cursor-grab gap-0.5 rounded-md border border-border bg-raised p-2 active:cursor-grabbing"
              title={sk.path}
            >
              <span className="flex items-center gap-1.5 text-label font-medium">
                <GripVertical className="size-3.5 text-faint" />
                <BookOpen className="size-3.5 text-primary" />
                <span className="min-w-0 flex-1 truncate">{sk.name}</span>
                {live.length > 0 ? (
                  <Tip label={`Running: ${live.map((id) => app.sessions.find((s) => s.id === id)?.name).join(", ")}`}>
                    <button type="button" className="rounded px-1 text-micro text-success hover:bg-hover" onClick={() => flash(live)}>
                      ● {live.length}
                    </button>
                  </Tip>
                ) : null}
              </span>
              <span className="truncate text-micro text-faint">{sk.description ?? ((sk.capabilities ?? []).join(", ") || sk.method)}</span>
            </li>
          );
        })}
        {skills.length === 0 ? <li className="px-1 text-caption text-faint">No helpers on the map. Tick "On the map" for the ones you use in Helpers.</li> : null}
      </ul>
      <div className="border-t border-border p-2">
        <Button size="sm" className="w-full" onClick={() => app.setNav("skills")}>
          Manage helpers →
        </Button>
      </div>
    </aside>
  );
}
