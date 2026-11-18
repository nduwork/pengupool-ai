import { describe, expect, it } from "vitest";

import type { Update } from "../protocol/domain";
import { updateLine } from "./Update";

const u = (o: Partial<Update>): Update => ({ current: "0.12.0", newer: false, ...o });

describe("updateLine", () => {
  it("says nothing before a check", () => {
    expect(updateLine(null)).toBe("");
  });
  it("says newest only when GitHub answered and nothing is newer", () => {
    expect(updateLine(u({}))).toBe("This is the newest version.");
  });
  it("names a newer release", () => {
    expect(updateLine(u({ newer: true, latest: "0.13.0" }))).toBe("PenguPool 0.13.0 is available.");
  });
  it("shows an error in place of newest", () => {
    expect(updateLine(u({ error: "Could not reach GitHub to check for updates (timeout)" }))).toBe("Could not reach GitHub to check for updates (timeout)");
  });
  it("shows an error and the installable newer release", () => {
    expect(updateLine(u({ newer: true, latest: "0.13.0", error: "PenguPool 0.13.1 is out, but its release has no disk image this app can install yet (x). Try again in a few minutes, or download it from the release page." }))).toBe(
      "PenguPool 0.13.1 is out, but its release has no disk image this app can install yet (x). Try again in a few minutes, or download it from the release page. PenguPool 0.13.0 is available.",
    );
  });
});
