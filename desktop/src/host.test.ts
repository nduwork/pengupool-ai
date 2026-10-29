// node --test src/host.test.ts (npm test).
import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { isStoreDaemon } from "./hostversion.ts";

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

// The guarantee the owner asked for: each run of the app talks only to the host it started, so the
// host is always the app's build. Runs the real Host (bundled by esbuild, as the app is); only
// probe, start, stop and the process listing are stubbed.
test("Host.ensure never keeps a host this run did not start; quit ends or hands over its own", async () => {
  const { buildSync } = await import("esbuild");
  const dir = mkdtempSync(join(tmpdir(), "pp-host-"));
  try {
    const out = join(dir, "host.mjs");
    buildSync({ entryPoints: [join(import.meta.dirname, "host.ts")], bundle: true, platform: "node", format: "esm", outfile: out, logLevel: "silent" });
    const { Host } = (await import(out)) as typeof import("./host.ts");
    const h = new Host(process.execPath) as any; // any existing file stands in for the bundled binary
    const stops: [number[], string][] = [];
    let starts = 0;
    h.probe = async () => ({ url: "http://127.0.0.1:1", token: "t" });
    h.hostPids = async () => [4242]; // a host left by another run or build, answering the probe
    h.stop = async (pids: number[], sig = "SIGTERM") => {
      stops.push([pids, sig]);
      return pids.length;
    };
    h.start = async () => {
      starts++;
      h.own = process.pid; // a live pid stands in for this run's host
    };

    assert.notEqual(await h.ensure(), null);
    assert.deepEqual(stops, [[[4242], "SIGTERM"]], "the host this run did not start is stopped");
    assert.equal(starts, 1, "and the run starts its own");
    assert.notEqual(await h.ensure(), null);
    assert.equal(starts, 1, "its own host is kept");

    await h.quit(true);
    assert.deepEqual(stops.at(-1), [[process.pid], "SIGUSR1"], "quitting to update hands the sessions over");
    h.own = process.pid;
    await h.quit();
    assert.deepEqual(stops.at(-1), [[process.pid], "SIGTERM"], "quitting ends the host and its sessions");
    await h.quit();
    assert.equal(stops.length, 3, "a run without a host has nothing to end");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
