import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RoleTag } from "./controls";

describe("role tag", () => {
  it("shows a specialist as SPEC with the tooltip Specialist", () => {
    const html = renderToStaticMarkup(createElement(RoleTag, { role: "helper" }));
    expect(html).toContain('title="Specialist"');
    expect(html).toContain(">SPEC</span>");
    expect(html).not.toMatch(/helper/i);
  });
});
