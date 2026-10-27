import { describe, expect, it } from "vitest";

import { parseRunLog } from "./runlog";

describe("parseRunLog", () => {
  it("splits runs, parses messages and folds status changes", () => {
    const runs = parseRunLog(
      [
        "09:00:00 status: idle",
        "09:48:03 session: started omp in /repo",
        "09:48:04 session: omp started (startup)",
        "10:41:44 lead → helper: hi ⏎ second line",
        "10:41:50 status: busy",
        "10:42:10 status: idle",
        "10:43:00 team: joined crew as helper",
        "a hand-edited line",
      ].join("\n"),
    );
    expect(runs.map((r) => r.title)).toEqual(["", "started omp in /repo"]);
    const e = runs[1].entries;
    expect(e[1]).toMatchObject({ kind: "message", from: "lead", to: "helper", text: "hi\nsecond line" });
    expect(e[2]).toMatchObject({ kind: "status", text: "busy → idle", time: "10:42:10" });
    expect(e[3]).toMatchObject({ kind: "team", text: "joined crew as helper" });
    expect(e[4]).toMatchObject({ kind: "note", time: "", text: "a hand-edited line" });
  });
});
