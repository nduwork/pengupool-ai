import { describe, expect, it } from "vitest";

import { shortTime } from "../lib/utils";
import { frameDoc, previewKind, withDiagrams } from "./Editors";

describe("file previews", () => {
  it("picks a preview by extension", () => {
    expect(previewKind("docs/README.md")).toBe("markdown");
    expect(previewKind("site/Index.HTML")).toBe("html");
    expect(previewKind("logo.svg")).toBe("image");
    expect(previewKind("spec.PDF")).toBe("pdf");
    expect(previewKind("main.go")).toBeNull();
  });

  it("renders Markdown with the file's folder as base, and never adds a base when scripts run", () => {
    const md = frameDoc("markdown", "# Title\n\n![x](img/a.png)", "http://h/api/raw/s/c/docs/");
    expect(md).toContain("<h1>Title</h1>");
    expect(md).toContain('<base href="http://h/api/raw/s/c/docs/">');
    expect(frameDoc("html", "<html><head><title>t</title></head></html>", "http://h/x/")).toContain('<head><base href="http://h/x/">');
    expect(frameDoc("html", "<p>hi</p>", undefined)).toBe("<p>hi</p>");
  });

  it("leaves a document without Mermaid blocks as it is", async () => {
    const md = frameDoc("markdown", "```go\nx := 1\n```", undefined);
    expect(await withDiagrams(md, false)).toBe(md);
  });

  it("shows a modification time as ls long-iso does", () => {
    expect(shortTime(new Date(2026, 9, 19, 14, 2).toISOString())).toBe("2026-10-19 14:02");
    expect(shortTime(new Date(2024, 2, 3, 9, 30).toISOString())).toBe("2024-03-03 09:30");
    expect(shortTime(undefined)).toBe("");
  });
});
