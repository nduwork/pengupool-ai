// The kind a browser announces in `hello` decides whether it may ever take the size lease: an
// `ios` client is view-only (docs/protocol/v1.md section 4), so an iOS browser must say `ios`
// rather than `browser` or the pane would claim the PTY.
import { afterEach, describe, expect, it, vi } from "vitest";

import { detectClientKind } from "./connection";

function withUserAgent(ua: string, maxTouchPoints = 0): void {
  vi.stubGlobal("navigator", { userAgent: ua, maxTouchPoints });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("detectClientKind", () => {
  it("announces ios for an iPhone", () => {
    withUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15");
    expect(detectClientKind()).toBe("ios");
  });

  it("announces ios for an iPadOS browser that reports a desktop user agent", () => {
    withUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15", 5);
    expect(detectClientKind()).toBe("ios");
  });

  it("announces browser for a desktop without touch points", () => {
    withUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/537.36", 0);
    expect(detectClientKind()).toBe("browser");
  });
});
