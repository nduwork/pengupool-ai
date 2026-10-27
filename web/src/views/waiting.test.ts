import { describe, expect, it } from "vitest";

import { checkinWaiters, waitingNote } from "./waiting";

describe("checkinWaiters", () => {
  const gate = (session_id: string, kind = "checkin") => ({ session_id, kind });
  const idle = () => "idle";
  it("lists a member with an open check-in", () => {
    expect(checkinWaiters(["a"], [gate("a")], idle)).toEqual(["a"]);
  });
  it("skips one that is working and one with no check-in", () => {
    expect(checkinWaiters(["a"], [gate("a")], () => "working")).toEqual([]);
    expect(checkinWaiters(["a"], [gate("a", "permission")], idle)).toEqual([]);
  });
  it("judges each of two members on their own", () => {
    expect(checkinWaiters(["a", "b"], [gate("a")], idle)).toEqual(["a"]);
  });
});

describe("waitingNote", () => {
  it("names the dialog a session waits on, and is nothing otherwise", () => {
    expect(waitingNote("folder trust")).toBe("waiting on folder trust");
    expect(waitingNote("")).toBeUndefined();
    expect(waitingNote(undefined)).toBeUndefined();
  });
});
