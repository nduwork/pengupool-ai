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

// The guarantee the owner asked for: the app never keeps a host that is not its own build. Runs the
// real Host (bundled by esbuild, as the app is) against a stand-in binary and store; only probe, stop
// and start are stubbed, so ensure() and outdated() are the shipped code.
test("Host.ensure restarts a host that is not this build, and keeps one that is", async () => {
  const { buildSync } = await import("esbuild");
  const dir = mkdtempSync(join(tmpdir(), "pp-host-"));
  const prevHome = process.env.PENGUPOOL_HOME;
  try {
    const out = join(dir, "host.mjs");
    buildSync({ entryPoints: [join(import.meta.dirname, "host.ts")], bundle: true, platform: "node", format: "esm", outfile: out, logLevel: "silent" });
    const { Host } = (await import(out)) as typeof import("./host.ts");
    const bin = join(dir, "pengupool");
    writeFileSync(bin, '#!/bin/sh\n[ "$1" = version ] && echo "pengupool 0.9.2+new (protocol v1)"\n', { mode: 0o755 });
    process.env.PENGUPOOL_HOME = join(dir, "home");
    const run = join(dir, "home", "v2", "run");
    mkdirSync(run, { recursive: true });

    const ensure = async (running: string | null) => {
      if (running === null) rmSync(join(run, "host.version"), { force: true });
      else writeFileSync(join(run, "host.version"), running + "\n");
      const h = new Host(bin) as any;
      let stops = 0;
      h.probe = async () => ({ url: "http://127.0.0.1:1", token: "t" });
      h.stop = async () => {
        stops++;
        writeFileSync(join(run, "host.version"), "pengupool 0.9.2+new (protocol v1)\n"); // the bundled host starts
        return 1;
      };
      h.waitDown = async () => {};
      h.start = async () => {};
      assert.notEqual(await h.ensure(), null);
      return stops;
    };
    assert.equal(await ensure("pengupool 0.8.5+old (protocol v1)"), 1, "an older host is restarted");
    assert.equal(await ensure(null), 1, "a host that predates host.version is restarted");
    assert.equal(await ensure("pengupool 0.9.2+new (protocol v1)"), 0, "this build's host is kept");
  } finally {
    if (prevHome === undefined) delete process.env.PENGUPOOL_HOME;
    else process.env.PENGUPOOL_HOME = prevHome;
    rmSync(dir, { recursive: true, force: true });
  }
});
