import { describe, expect, it } from "vitest";

import { pairBlocker, type TailscaleCheck } from "./museReady";

const row = (id: string, ok: boolean): TailscaleCheck => ({ id, label: `Step ${id}`, ok, detail: `detail ${id}` });

describe("pairBlocker", () => {
  it("allows a code once Serve is set up", () => {
    expect(pairBlocker("https://mac.tail1234.ts.net", [row("login", false)])).toBeNull();
  });
  it("names the first failing step while Serve isn't set up", () => {
    expect(pairBlocker("", [row("installed", true), row("login", false), row("serve", false)])).toBe("Step login: detail login");
    expect(pairBlocker(undefined, [row("installed", true), row("serve", false)])).toBe("Step serve: detail serve");
  });
  it("still blocks when every step passes but no Serve name is saved", () => {
    expect(pairBlocker("", [row("installed", true)])).toBe("Tailscale Serve isn't set up");
    expect(pairBlocker(undefined, [])).toBe("Tailscale Serve isn't set up");
  });
});
