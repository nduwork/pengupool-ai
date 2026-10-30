import { useEffect, useState, type ReactElement } from "react";

import { Unplug } from "lucide-react";
import { harnessLabel, HOOKS_LONG, hooksAction, shownStatus, statusStyle, useApp } from "../../app/ctx";
import type { SessionView } from "../../protocol/control";
import { parseRunLog } from "../runlog";

// Small pieces shared by the map's session tiles.

/** A dot on a tile or row: output arrived since the owner last viewed the session. */
export function UnreadDot({ id }: { id: string }): ReactElement | null {
  const app = useApp();
  return app.unread.has(id) ? <span role="img" aria-label="Unread output" className="size-2 shrink-0 rounded-full bg-primary" /> : null;
}

/** The hover card of a tile or row: status, last message, branch and working directory. */
export function SessionHover({ s }: { s: SessionView }): ReactElement {
  const app = useApp();
  const [last, setLast] = useState("");
  useEffect(() => {
    void app.conn.request<{ content: string }>("runlog.read", { session_id: s.id }).then(
      (r) => {
        const e = parseRunLog(r.content).flatMap((x) => x.entries).filter((x) => x.kind === "message").pop();
        setLast(e?.text ?? "");
      },
      () => undefined,
    );
  }, [app.conn, s.id]);
  return (
    <div className="grid max-w-72 grid-cols-1 gap-0.5">
      <strong>{s.name}</strong>
      <span className={statusStyle(shownStatus(s, app.gates)).text}>
        {statusStyle(shownStatus(s, app.gates)).label} · {harnessLabel(s.harness)}
      </span>
      {s.question !== undefined && s.question !== "" ? <span className="line-clamp-3 text-foreground [overflow-wrap:anywhere]">{s.question}</span> : null}
      {s.hooks_missing === true ? (
        <span className="flex items-start gap-1 text-foreground">
          <Unplug className="mt-0.5 size-3 shrink-0" aria-hidden />
          {HOOKS_LONG} {hooksAction(s.harness)}
        </span>
      ) : null}
      {last !== "" ? <span className="line-clamp-3 text-muted-foreground [overflow-wrap:anywhere]">{last}</span> : null}
      {s.branch !== undefined && s.branch !== "" ? <span className="font-mono text-muted-foreground">{s.branch}</span> : null}
      <span className="truncate font-mono text-faint">{s.cwd}</span>
    </div>
  );
}
