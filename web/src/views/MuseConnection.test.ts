import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RefusedList } from "./MuseConnection";
import { refusedReason, serveLine } from "./museReady";

describe("the Muse connection panel", () => {
  it("says each refusal in plain words", () => {
    expect(refusedReason("wrong_host")).toBe("a device used the wrong address");
    expect(refusedReason("revoked_token")).toBe("a revoked device tried to connect");
    expect(refusedReason("unknown_token")).toBe("an unknown token");
    expect(refusedReason("bad_code")).toBe("a wrong or expired pairing code");
    expect(refusedReason("pairing_paused")).toBe("pairing was paused after too many wrong codes");
  });
  it("shows where devices reach the host, or what is wrong", () => {
    expect(serveLine({ state: "ok", url: "https://mac.tail1234.ts.net" })).toBe("Reachable at https://mac.tail1234.ts.net");
    expect(serveLine({ state: "problem", problem: "Logged in: Backend state: Stopped", check: "login" })).toBe("Logged in: Backend state: Stopped");
  });
  it("lists refusals newest first, naming the device, with long hosts wrapping", () => {
    const host = "a-very-long-machine-name-that-goes-on-and-on.tail1234567.ts.net:443";
    const html = renderToStaticMarkup(
      createElement(RefusedList, {
        refused: [
          { at: "2026-11-18T10:00:00Z", reason: "wrong_host", host, device: "phone1" },
          { at: "2026-11-18T10:05:00Z", reason: "revoked_token", device: "old-ipad" },
        ],
      }),
    );
    expect(html.indexOf("a revoked device tried to connect (old-ipad)")).toBeLessThan(html.indexOf("a device used the wrong address (phone1)"));
    expect(html).toContain(host);
    expect(html).toContain("[overflow-wrap:anywhere]");
  });
  it("says when nothing was refused", () => {
    expect(renderToStaticMarkup(createElement(RefusedList, { refused: [] }))).toContain("No refused requests since the host started.");
  });
});
