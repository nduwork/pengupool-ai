import { describe, expect, it } from "vitest";

import { cn } from "./utils";

describe("cn", () => {
  it("keeps a theme font size beside a colour, and lets a later size replace it", () => {
    expect(cn("text-label", "text-danger")).toBe("text-label text-danger");
    expect(cn("text-caption", "text-muted-foreground")).toBe("text-caption text-muted-foreground");
    expect(cn("text-label", "text-caption")).toBe("text-caption");
    expect(cn("text-danger", "text-success")).toBe("text-success");
  });
});
