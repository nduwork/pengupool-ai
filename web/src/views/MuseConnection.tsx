import type { ReactElement } from "react";

import { shortTime } from "../lib/utils";
import { refusedReason, type MuseRefusal } from "./museReady";

/** Settings › Muse app: the requests a device made that the host refused since it started. */
export function RefusedList({ refused }: { refused: MuseRefusal[] }): ReactElement {
  return (
    <div className="border-t border-border py-3">
      <p className="text-label font-medium">Recent refused requests</p>
      <p className="text-caption text-muted-foreground">The last 20, kept in memory: the list starts empty each time the host starts.</p>
      {refused.length === 0 ? (
        <p className="mt-2 text-caption text-muted-foreground">No refused requests since the host started.</p>
      ) : (
        <ul aria-label="Recent refused requests" className="mt-2 flex flex-col gap-1">
          {[...refused].reverse().map((r, i) => (
            <li key={`${r.at}-${i}`} className="min-w-0 text-caption [overflow-wrap:anywhere]">
              <span className="text-muted-foreground">{shortTime(r.at)}</span> · {refusedReason(r.reason)}
              {r.device !== undefined && r.device !== "" ? ` (${r.device})` : ""}
              {r.host !== undefined && r.host !== "" ? <span className="text-muted-foreground"> · {r.host}</span> : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
