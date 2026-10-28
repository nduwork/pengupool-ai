// node --test src/host.test.ts (npm test): the version check is pure and read-only, never run against a host.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { hostOutdated, readHostVersion } from "./hostversion.ts";

test("hostOutdated: only the bundled build is current", () => {
  assert.equal(hostOutdated("pengupool 0.8.0+abc (protocol v1)", "pengupool 0.8.0+abc (protocol v1)"), false);
  assert.equal(hostOutdated("pengupool 0.5.0+abc (protocol v1)", "pengupool 0.8.0+abc (protocol v1)"), true);
  assert.equal(hostOutdated("pengupool 0.8.0+abc (protocol v1)", "pengupool 0.8.0+def (protocol v1)"), true);
  assert.equal(hostOutdated(null, "pengupool 0.8.0+abc (protocol v1)"), true);
});

test("readHostVersion: the daemon's line, or null when it predates the file", () => {
  const dir = mkdtempSync(join(tmpdir(), "pp-store-"));
  try {
    assert.equal(readHostVersion(dir), null);
    mkdirSync(join(dir, "run"), { recursive: true });
    writeFileSync(join(dir, "run", "host.version"), "pengupool 0.8.0+abc (protocol v1)\n");
    assert.equal(readHostVersion(dir), "pengupool 0.8.0+abc (protocol v1)");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
