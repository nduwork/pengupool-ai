import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type AppCtx, Ctx } from "../app/ctx";
import type { SessionView } from "../protocol/control";
import type { Approval } from "../protocol/domain";
import { PANEL, WaitingList } from "./Approvals";
import type { OpenGate } from "./inbox";

// Tests run without a browser layout engine, so nothing here measures widths: they check that the
// long text and the buttons carry the classes that wrap them inside the panel, and that none carries
// a class that would keep them on one line.

const question = ("QSTART " + "which migration strategy should I use for the orders table ".repeat(20) + "x".repeat(300)).slice(0, 1500);
const options = ["OPT1", "OPT2", "OPT3"].map((o) => o + "a".repeat(76)); // 80 runes each, the host's limit
const reply = "CSTART " + "the table refactor is done and the tests pass ".repeat(25);

function render(): string {
  const session = { id: "s1", name: "api-builder", state: "running", status: "idle", harness: "claude" } as SessionView;
  const ask = { id: "A1", kind: "ask", state: "pending", session_id: "s1", detail: question, options, created_at: new Date().toISOString() } as Approval;
  const gate: OpenGate = { id: 7, session_id: "s1", agent: "api-builder", kind: "checkin", question: reply, floor: false };
  const app = {
    sessions: [session],
    domain: { approvals: [ask], messages: [], teams: [], tasks: [], repos: [], skills: [] },
    gates: [gate],
    spots: [{ id: 9, agent: "api-builder", question: "SSTART " + "y".repeat(500) }],
    call: async () => undefined,
  } as unknown as AppCtx;
  return renderToStaticMarkup(createElement(Ctx.Provider, { value: app }, createElement(WaitingList)));
}

const classOf = (html: string, marker: string): string => {
  const m = new RegExp(`<(?:div|button)[^>]*class="([^"]*)"[^>]*>${marker}`).exec(html);
  expect(m, marker).not.toBeNull();
  return m?.[1] ?? "";
};

describe("Waiting on you panel", () => {
  const html = render();

  it("wraps a long question, the check-in's reply and a go-ahead, never cutting them to one line", () => {
    for (const marker of ["QSTART", "CSTART", "Was this go-ahead right\\? SSTART"]) {
      const cls = classOf(html, marker);
      expect(cls).toContain("whitespace-pre-wrap");
      expect(cls).toContain("[overflow-wrap:anywhere]");
      expect(cls).not.toMatch(/\btruncate\b|whitespace-nowrap/);
    }
    expect(html).toContain(question);
  });

  it("wraps long options inside their buttons, one default button per row", () => {
    for (const o of options) {
      const cls = classOf(html, o);
      expect(cls).toContain("whitespace-normal");
      expect(cls).toContain("[overflow-wrap:anywhere]");
      expect(cls).not.toMatch(/\btruncate\b|whitespace-nowrap/);
    }
    for (const row of html.split('role="listitem"').slice(1)) expect(row.match(/bg-primary /g)?.length ?? 0).toBeLessThanOrEqual(1);
  });

  it("puts actions on their own wrapping line", () => {
    expect(html).toMatch(/<div class="flex flex-wrap justify-end gap-1.5"><button[^>]*>Go on<\/button>/);
    expect(html).toMatch(/<div class="flex flex-wrap justify-end gap-1.5"><button[^>]*><svg[^]*?Right<\/button>/);
    expect(html).toContain('role="list"');
  });

  it("keeps the panel inside the window: a max width and a max height", () => {
    expect(PANEL).toContain("w-[440px]");
    expect(PANEL).toContain("max-w-[calc(100vw-16px)]");
    expect(PANEL).toContain("max-h-[min(36rem,var(--radix-popover-content-available-height))]");
    expect(PANEL).toContain("flex-col");
  });
});
