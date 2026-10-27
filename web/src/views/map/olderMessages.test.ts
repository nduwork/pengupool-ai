import { describe, expect, it } from "vitest";

import { olderThan } from "./olderMessages";

const m = (at: string) => ({ from: "a", to: "b", at, text: at });

describe("olderThan", () => {
  it("keeps only what is older than the oldest shown, newest first", () => {
    const got = olderThan([m("2026-01-01T00:01:00Z"), m("2026-01-01T00:03:00Z"), m("2026-01-01T00:05:00Z")], [{ at: "2026-01-01T00:04:00Z" }, { at: "2026-01-01T00:03:00Z" }]);
    expect(got.map((x) => x.at)).toEqual(["2026-01-01T00:01:00Z"]);
  });
  it("is everything, newest first, when nothing is shown", () => {
    expect(olderThan([m("2026-01-01T00:01:00Z"), m("2026-01-01T00:02:00Z")], []).map((x) => x.at)).toEqual(["2026-01-01T00:02:00Z", "2026-01-01T00:01:00Z"]);
  });
});
