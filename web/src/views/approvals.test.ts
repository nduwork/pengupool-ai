import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type AppCtx, Ctx } from "../app/ctx";
import type { SessionView } from "../protocol/control";
import type { Approval } from "../protocol/domain";
import { PANEL, refocusRow, WaitingList } from "./Approvals";
import type { OpenGate } from "./inbox";

// Tests run without a browser layout engine, so nothing here measures widths: they check that the
// long text and the buttons carry the classes that wrap them inside the panel, and that none carries
// a class that would keep them on one line.

const question = ("QSTART " + "which migration strategy should I use for the orders table ".repeat(20) + "x".repeat(300)).slice(0, 1500);
const options = ["OPT1", "OPT2", "OPT3"].map((o) => o + "a".repeat(76)); // 80 runes each, the host's limit
const reply = "CSTART " + "the table refactor is done and the tests pass ".repeat(25);

function render(): string {
  const session = { id: "s1", name: "api-builder", state: "running", status: "idle", harness: "claude" } as SessionView;
  // A member's ask between agents with long names: asker → recipient must wrap, not run out sideways.
  const asker = { id: "s2", name: "pengupool-agent-hub-codex", state: "running", status: "idle", harness: "claude" } as SessionView;
  const recipient = { id: "s3", name: "team-lead-agent-claude", state: "running", status: "idle", harness: "claude" } as SessionView;
  const memberAsk = { id: "m1", team_id: "t1", from: "s2", to: "s3", type: "ask", text: "MSTART can I get a staging snapshot", at: new Date().toISOString(), escalated_at: new Date().toISOString() };
  const ask = { id: "A1", kind: "ask", state: "pending", session_id: "s1", detail: question, options, created_at: new Date().toISOString() } as Approval;
  const gate: OpenGate = { id: 7, session_id: "s1", agent: "api-builder", kind: "checkin", question: reply, floor: false };
  const app = {
    sessions: [session, asker, recipient],
    domain: { approvals: [ask], messages: [memberAsk], teams: [], tasks: [], repos: [], skills: [] },
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

  it("wraps long options inside their buttons, none of them filled, one default button per row", () => {
    for (const o of options) {
      const cls = classOf(html, o);
      expect(cls).toContain("whitespace-normal");
      expect(cls).toContain("[overflow-wrap:anywhere]");
      expect(cls).not.toMatch(/\btruncate\b|whitespace-nowrap/);
      expect(cls).not.toMatch(/bg-primary /);
    }
    for (const row of html.split('role="listitem"').slice(1)) expect(row.match(/bg-primary /g)?.length ?? 0).toBeLessThanOrEqual(1);
  });

  it("puts actions on their own wrapping line", () => {
    expect(html).toMatch(/<div class="flex flex-wrap justify-end gap-1.5"><button[^>]*>Go on<\/button>/);
    expect(html).toMatch(/<div class="flex flex-wrap justify-end gap-1.5"><button[^>]*><svg[^]*?Right<\/button>/);
    expect(html).toContain('role="list"');
  });

  it("wraps a meta line with long agent names, the names uncut", () => {
    const row = html.split('role="listitem"').find((r) => r.includes("pengupool-agent-hub-codex")) ?? "";
    expect(row).toContain("team-lead-agent-claude");
    // The top line itself does not wrap (the icon stays beside the names); the names and team inside it do.
    const top = /<div class="([^"]*)"><span class="[^"]*"><svg/.exec(row)?.[1] ?? "";
    expect(top).toContain("items-start");
    expect(top).not.toContain("flex-wrap");
    const names = /<\/svg><\/span><span class="([^"]*)">/.exec(row)?.[1] ?? "";
    expect(names).toContain("flex-wrap");
    expect(names).toContain("flex-1");
    const who = /<span class="([^"]*)"><strong[^>]*>pengupool-agent-hub-codex/.exec(row)?.[1] ?? "";
    expect(who).toContain("flex-wrap");
    expect(who).not.toContain("shrink-0");
    expect(row).not.toMatch(/\btruncate\b[^"]*"><strong/);
  });

  it("keeps the panel inside the window: a max width and a max height", () => {
    expect(PANEL).toContain("w-[440px]");
    expect(PANEL).toContain("max-w-[calc(100vw-16px)]");
    expect(PANEL).toContain("max-h-[min(36rem,var(--radix-popover-content-available-height))]");
    expect(PANEL).toContain("flex-col");
  });

  it("moves focus from a row that left to the row now at its index (the row, not its first button), else the last row, else the panel", () => {
    expect(html).toMatch(/role="listitem" aria-label="[^"]*" tabindex="-1" class="[^"]*focus-visible:ring-2/);
    expect(PANEL).toContain("outline-none");
    expect(refocusRow(1, 3)).toBe(1);
    expect(refocusRow(2, 2)).toBe(1);
    expect(refocusRow(0, 0)).toBe(-1);
  });
});
