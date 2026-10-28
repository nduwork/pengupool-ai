// node --test src/host.test.ts (npm test): the version check is pure and read-only, never run against a host.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { hostOutdated, isStoreDaemon, readHostVersion } from "./hostversion.ts";

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

test("isStoreDaemon: only a PenguPool daemon of this store", () => {
  const store = "/Users/me/.pengupool-ai/v2";
  const app = "/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool";
  assert.equal(isStoreDaemon(`${app} daemon --store ${store}`, store), true);
  assert.equal(isStoreDaemon("/Volumes/PenguPool-AI 4/PenguPool AI.app/Contents/Resources/bin/pengupool daemon", store), true); // the default store
  assert.equal(isStoreDaemon(`${app} daemon --store /tmp/other/v2`, store), false);
  assert.equal(isStoreDaemon(`${app} daemon --store ${store}-old`, store), false);
  assert.equal(isStoreDaemon(`${app} worker --session-id x`, store), false);
  assert.equal(isStoreDaemon(`vim '${app} daemon --store ${store}'`, store), false);
  assert.equal(isStoreDaemon(`sh -c '${app} daemon --store ${store}'`, store), false);
  assert.equal(isStoreDaemon(`/usr/bin/pengupool2 daemon --store ${store}`, store), false);
});
