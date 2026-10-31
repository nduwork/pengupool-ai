import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Ctx, type AppCtx } from "../../app/ctx";
import { TIP_CONTENT } from "../../components/ui/controls";
import type { SessionView } from "../../protocol/control";
import { SessionHover } from "./SessionBits";

describe("map hover card", () => {
  it("shows its content whole, and the tip scrolls within the room it has instead of being cut", () => {
    const long = "Shall I push the release branch? ".repeat(30);
    const s = { id: "a", name: "web", state: "running", status: "approval", harness: "claude", question: long, cwd: "/a/very/long/path/".repeat(10), cols: 80, rows: 24 } as SessionView;
    const app = { conn: { request: () => new Promise(() => undefined) }, gates: [] } as unknown as AppCtx;
    const html = renderToStaticMarkup(createElement(Ctx.Provider, { value: app }, createElement(SessionHover, { s })));
    expect(html).toContain(long.trim());
    expect(html).not.toMatch(/line-clamp|truncate/);
    expect(TIP_CONTENT).toContain("max-h-[var(--radix-tooltip-content-available-height)]");
    expect(TIP_CONTENT).toContain("overflow-y-auto");
  });
});
