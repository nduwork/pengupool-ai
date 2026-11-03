import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RoleTag } from "./controls";

describe("role tag", () => {
  it("shows a helper as Helper and an sme as Builder", () => {
    const helper = renderToStaticMarkup(createElement(RoleTag, { role: "helper" }));
    expect(helper).toContain('title="Helper: an agent from the helper pool"');
    expect(helper).toContain(">Helper</span>");
    const sme = renderToStaticMarkup(createElement(RoleTag, { role: "sme" }));
    expect(sme).toContain(">Builder</span>");
    expect(sme).not.toMatch(/SME|Specialist/);
  });
});
