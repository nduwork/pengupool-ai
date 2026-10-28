import { Trash2 } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { toast } from "sonner";

import { useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Empty } from "../components/ui/controls";
import type { JournalEntry, Skill } from "../protocol/domain";

// A helper's experience: its journal, one entry per finished job (title, date and the lesson it
// carries over; the full text, the harness's own summary of the work, on click). The helper writes an entry by reporting "Done:" to
// its parent; the owner can delete one.

/** A helper's identity in the record: its repository key (the host's agentOf). */
export const agentKey = (s: Skill): string => s.key ?? `local:${s.id}`;

export function HelperExperience({ skill }: { skill: Skill }): ReactElement {
  const app = useApp();
  const agent = agentKey(skill);
  const [entries, setEntries] = useState<JournalEntry[] | null>(null);
  const [open, setOpen] = useState<JournalEntry | null>(null);
  const [rev, setRev] = useState(0);
  useEffect(() => {
    void app.call<JournalEntry[]>("experience.list", { agent }).then((r) => setEntries(r ?? []));
  }, [agent, rev]);
  const show = async (e: JournalEntry): Promise<void> => {
    if (open?.id === e.id) return setOpen(null);
    const full = await app.call<JournalEntry>("experience.get", { id: e.id });
    if (full !== undefined) setOpen(full);
  };
  const remove = async (e: JournalEntry): Promise<void> => {
    if (!window.confirm(`Delete "${e.title}"? ${skill.name} will not find it again.`)) return;
    if ((await app.call("experience.delete", { id: e.id })) === undefined) return;
    toast.success("Deleted");
    setOpen(null);
    setRev(rev + 1);
  };
  return (
    <div className="flex flex-col gap-3 p-4">
      <p className="text-caption text-muted-foreground">
        What {skill.name} did before: an entry for each job it reported done, its lesson and the summary of the work. It sees the best matches, with their lessons, whenever it gets a task.
      </p>
      {entries === null ? <p className="text-label text-faint">Loading…</p> : null}
      {entries?.length === 0 ? <Empty title="Nothing here yet.">An entry appears when it tells its parent &ldquo;Done: …&rdquo;.</Empty> : null}
      {entries?.map((e) => (
        <div key={e.id} className="rounded-md border border-border text-label">
          <div className="flex items-center gap-2 px-3 py-2">
            <button type="button" className="flex min-w-0 flex-1 items-baseline gap-2 text-left" aria-expanded={open?.id === e.id} onClick={() => void show(e)}>
              <span className="min-w-0 flex-1 truncate font-medium">{e.title}</span>
              <span className="shrink-0 text-caption text-faint">{new Date(e.at).toLocaleDateString()}</span>
            </button>
            <Button size="sm" variant="ghost" aria-label={`Delete ${e.title}`} onClick={() => void remove(e)}>
              <Trash2 />
            </Button>
          </div>
          {e.lesson ? <p className="-mt-1 px-3 pb-2 text-caption text-muted-foreground">Lesson: {e.lesson}</p> : null}
          {open?.id === e.id ? <pre className="max-h-96 overflow-auto whitespace-pre-wrap border-t border-border px-3 py-2 font-sans">{open.body}</pre> : null}
        </div>
      ))}
    </div>
  );
}
