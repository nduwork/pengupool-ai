import { Compass, ListChecks, Pencil, Plus, Search, ThumbsDown, ThumbsUp, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";

import { useApp } from "../app/ctx";
import { Button } from "../components/ui/button";
import { Empty, Input, Select, Textarea, submitOnEnter } from "../components/ui/controls";
import { Sheet } from "../components/ui/dialog";
import { cn } from "../lib/utils";
import { useSpotChecks } from "./Approvals";
import { groupLines, type Line, type ReviewItem } from "./compass";
import { Mascot } from "./Mascot";

// The owner's Compass (preference lines kept by the host in agents.db, not in any repository; each
// line's belief comes from the owner's own words and choices), as a panel beside the map. A helper's
// experience is on its page in Specialists.

const KINDS = ["preference", "restriction"] as const;
const STATE_STYLE: Record<Line["state"], string> = {
  confident: "text-success",
  learning: "text-info",
  fading: "text-warning",
};

export function CompassPanel(): ReactElement {
  const app = useApp();
  const [items, setItems] = useState<Line[] | null>(null);
  const [autonomy, setAutonomy] = useState(""); // the learned thresholds, as one belief
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState(false);
  const [rev, setRev] = useState(0);
  const [review, setReview] = useState<ReviewItem[]>([]);
  const { spots, verdict } = useSpotChecks();
  useEffect(() => {
    const t = window.setTimeout(() => {
      void app.call<{ lines: Line[]; autonomy: string }>("compass.list", { query }).then((r) => {
        setItems(r?.lines ?? []);
        setAutonomy(r?.autonomy ?? "");
      });
      void app.call<{ items: ReviewItem[] }>("compass.review").then((r) => setReview(r?.items ?? []));
    }, 150);
    return () => window.clearTimeout(t);
  }, [query, rev]);
  const reload = (): void => setRev(rev + 1);
  const groups = groupLines(items ?? []);
  return (
    <section className="flex h-full min-h-0 flex-col bg-panel" aria-label="Compass">
      <div className="flex shrink-0 flex-col gap-2 border-b border-border px-3 py-2">
        <div className="flex items-center gap-2">
          <Compass className="size-4 text-faint" />
          <h2 className="min-w-0 flex-1 truncate text-label font-semibold">Compass</h2>
          {review.length > 0 ? (
            <Button size="sm" variant="secondary" onClick={() => app.showDialog(<ReviewSheet items={review} onChange={reload} />)}>
              <ListChecks /> Review · {review.length}
            </Button>
          ) : null}
          <Button size="sm" variant="default" onClick={() => setAdding(true)} disabled={adding}>
            <Plus /> Add a line
          </Button>
        </div>
        <label className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-faint" />
          <Input className="pl-8" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search the Compass" aria-label="Search" />
        </label>
      </div>
      <div className="grid min-h-0 flex-1 grid-cols-1 content-start gap-3 overflow-auto p-3">
        {adding ? (
          <LineEditor
            onDone={(saved) => {
              setAdding(false);
              if (saved) reload();
            }}
          />
        ) : null}
        {autonomy !== "" ? <p className="text-caption text-muted-foreground">{autonomy}</p> : null}
        {spots.length > 0 ? (
          <div className="grid grid-cols-1 gap-1">
            <p className="text-caption font-medium text-faint">Spot checks</p>
            {spots.map((g) => (
              <div key={g.id} className="flex items-center gap-2 text-label">
                <span className="min-w-0 flex-1">
                  Was this go-ahead right? <strong className="font-semibold">{g.agent}</strong>: {g.question}
                </span>
                <Button size="sm" variant="default" aria-label="Right" onClick={() => void verdict(g.id, true).then(reload)}>
                  <ThumbsUp />
                </Button>
                <Button size="sm" aria-label="Wrong" onClick={() => void verdict(g.id, false).then(reload)}>
                  <ThumbsDown />
                </Button>
              </div>
            ))}
          </div>
        ) : null}
        {items === null ? <p className="text-label text-faint">Loading…</p> : null}
        {items?.length === 0 && !adding ? (
          <Empty icon={<Mascot />} title={query !== "" ? "No matches" : "No preferences yet"}>
            {query !== "" ? "Try other words." : "Add one, or just tell an agent how you want things: PenguPool notes it."}
          </Empty>
        ) : null}
        {groups.map((g) => (
          <div key={g.title} className="grid grid-cols-1 gap-2">
            <p className="text-caption font-medium text-faint">{g.title}</p>
            {g.lines.map((l) => (
              <LineRow key={l.uid} l={l} onChange={reload} />
            ))}
          </div>
        ))}
      </div>
    </section>
  );
}

function LineRow({ l, onChange }: { l: Line; onChange: () => void }): ReactElement {
  const app = useApp();
  const [editing, setEditing] = useState(false);
  if (editing)
    return (
      <LineEditor
        l={l}
        onDone={(saved) => {
          setEditing(false);
          if (saved) onChange();
        }}
      />
    );
  return (
    <article className="group grid grid-cols-1 gap-1.5 rounded-lg border border-border bg-raised p-3">
      <header className="flex min-w-0 items-center gap-2 text-caption">
        <span
          className={cn("font-semibold capitalize", STATE_STYLE[l.state])}
          title={`belief ${l.mu.toFixed(2)}, evidence ${l.n.toFixed(1)}, uncertainty ${l.sigma.toFixed(2)}`}
        >
          {l.state}
        </span>
        <span className="tabular-nums text-faint">
          belief {l.mu.toFixed(2)} · evidence {l.n.toFixed(1)}
        </span>
        <span className="text-faint">{l.kind}</span>
        <span className="ml-auto min-w-0 truncate text-faint" title={`${l.source === "owner" ? "added by you · " : ""}${l.created_at.slice(0, 10)}`}>
          {l.source === "owner" ? "by you · " : ""}
          {l.created_at.slice(5, 10)}
        </span>
      </header>
      <p className="whitespace-pre-wrap text-body leading-relaxed">{l.body}</p>
      {l.condition !== undefined && l.condition !== "" ? <p className="text-caption text-muted-foreground">When {l.condition}</p> : null}
      <footer className="flex flex-wrap items-center gap-1.5 pt-1">
        <span className="flex-1" />
        <span className="flex gap-1 opacity-60 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
          <Button size="sm" variant="ghost" onClick={() => setEditing(true)}>
            <Pencil /> Edit
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={async () => {
              await app.call("compass.delete", { uid: l.uid });
              onChange();
            }}
          >
            <Trash2 /> Delete
          </Button>
        </span>
      </footer>
    </article>
  );
}

function LineEditor({ l, onDone }: { l?: Line; onDone: (saved: boolean) => void }): ReactElement {
  const app = useApp();
  const [f, setF] = useState({ kind: l?.kind ?? "preference", body: l?.body ?? "", condition: l?.condition ?? "" });
  return (
    <form
      className="grid grid-cols-1 gap-2 rounded-lg border border-primary/40 bg-raised p-3"
      onSubmit={async (e) => {
        e.preventDefault();
        const r = await app.call("compass.save", { uid: l?.uid, ...f });
        if (r !== undefined) onDone(true);
      }}
    >
      <Select className="w-36" value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value })} aria-label="Kind">
        {KINDS.map((k) => (
          <option key={k} value={k}>
            {k[0].toUpperCase() + k.slice(1)}
          </option>
        ))}
      </Select>
      <Textarea
        autoFocus
        className="min-h-24"
        value={f.body}
        onChange={(e) => setF({ ...f, body: e.target.value })}
        placeholder="What do you prefer?"
        onKeyDown={submitOnEnter}
        aria-label="Preference"
      />
      <Input value={f.condition} onChange={(e) => setF({ ...f, condition: e.target.value })} placeholder="When it applies (empty: always)" aria-label="Condition" />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => onDone(false)}>
          Cancel
        </Button>
        <Button type="submit" variant="default" disabled={f.body.trim() === ""}>
          {l === undefined ? "Add line" : "Save"}
        </Button>
      </div>
    </form>
  );
}

/** The Compass review: broad lines first, then merges. Each is the owner's call; nothing here runs on its own. */
function ReviewSheet({ items, onChange }: { items: ReviewItem[]; onChange: () => void }): ReactElement {
  const app = useApp();
  const [left, setLeft] = useState(items);
  const done = (i: number): void => {
    setLeft((l) => l.filter((_, j) => j !== i));
    onChange();
  };
  // A conflict resolved may settle others that shared a line: the review is fetched again.
  const refresh = async (): Promise<void> => {
    const r = await app.call<{ items: ReviewItem[] }>("compass.review");
    setLeft(r?.items ?? []);
    onChange();
  };
  return (
    <Sheet title="Review the Compass" description="Broad lines first, then lines that contradict. Nothing changes until you choose." onClose={() => app.showDialog(null)} wide>
      {left.length === 0 ? <p className="text-label text-muted-foreground">Nothing left to review.</p> : null}
      <ul className="grid gap-3">
        {left.map((it, i) => (
          <li
            key={it.lines[0].uid + it.kind}
            className={cn("grid gap-1.5 rounded-lg border p-3 text-label", it.kind === "broad" ? "border-warning/50 bg-warning/5" : "border-border")}
          >
            <p className="text-caption font-semibold">{`${{ broad: "Broad", conflict: "Conflict", merge: "Merge" }[it.kind]}: ${it.why}`}</p>
            <ul className="grid gap-1 text-muted-foreground">
              {it.lines.map((l) => (
                <li key={l.uid} className="flex items-start gap-2 [overflow-wrap:anywhere]">
                  <span className="min-w-0 flex-1">
                    {l.body}
                    {it.kind === "conflict" ? <span className="text-faint"> · when {l.condition || "always"}</span> : null}
                  </span>
                  {it.kind === "conflict" ? (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={async () => {
                        await app.call("compass.keep_one", { keep: l.uid, lines: it.lines, edges: it.edges ?? [] });
                        await refresh(); // after a stale refusal too: the owner sees the review as it is now
                      }}
                    >
                      Keep this
                    </Button>
                  ) : null}
                </li>
              ))}
            </ul>
            {it.kind === "conflict" ? <p>Keeping one deletes the lines that contradict it, with their evidence.</p> : null}
            {it.kind === "merge" ? <p>Becomes {it.patterns?.map((p) => `\`${p}\``).join(", ")}, keeping the evidence.</p> : null}
            <div className="flex flex-wrap justify-end gap-2">
              {it.kind === "broad" ? (
                <>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await app.call("compass.save", { uid: it.lines[0].uid, kind: "preference", body: it.lines[0].body })) !== undefined && done(i)}
                  >
                    Keep as is
                  </Button>
                  <Button size="sm" variant="danger" onClick={async () => (await app.call("compass.delete", { uid: it.lines[0].uid })) !== undefined && done(i)}>
                    Remove (answers are kept per subcommand from now on)
                  </Button>
                </>
              ) : it.kind === "merge" ? (
                <Button
                  size="sm"
                  variant="default"
                  onClick={async () => (await app.call("compass.apply_merge", { from: it.lines.map((l) => l.uid), patterns: it.patterns })) !== undefined && done(i)}
                >
                  Merge
                </Button>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
    </Sheet>
  );
}
