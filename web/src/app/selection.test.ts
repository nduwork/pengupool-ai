import { describe, expect, it } from "vitest";

import { selectionWait } from "./selection";

describe("selectionWait", () => {
  it("waits up to 5 s for the session select named", () => {
    const just = { id: "a", at: 1000 };
    expect(selectionWait(just, "a", 1000)).toBe(5000);
    expect(selectionWait(just, "a", 4000)).toBe(2000);
    expect(selectionWait(just, "a", 6000)).toBe(0);
    expect(selectionWait(just, "a", 60000)).toBe(0);
  });
  it("does not wait for anything else", () => {
    expect(selectionWait({ id: "a", at: 1000 }, "b", 1000)).toBe(0);
    expect(selectionWait(null, "a", 1000)).toBe(0);
    expect(selectionWait({ id: "a", at: 1000 }, null, 1000)).toBe(0);
  });
});
