// node --test src/host.test.ts (npm test).
import assert from "node:assert/strict";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
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
  const dir = mkdtempSync(join(tmpdir(), "pp-host-"));
  const prevHome = process.env.PENGUPOOL_HOME;
  process.env.PENGUPOOL_HOME = join(dir, "home");
  try {
    const h = await bundledHost(dir);
    const stops: [number[], string][] = [];
    let starts = 0;
    let answers = true;
    let probes = 0;
    h.probe = async () => (probes++, answers ? { url: "http://127.0.0.1:1", token: "t" } : null);
    h.hostPids = async () => [{ pid: 4242, owned: false }]; // a host started by hand, answering the probe
    h.stop = async (pids: number[], sig = "SIGTERM") => {
      if (pids.length > 0) stops.push([pids, sig]);
      return pids.length;
    };
    h.start = async () => {
      starts++;
      h.own = process.pid; // a live pid stands in for this run's host
    };
    const marker = join(dir, "home", "v2", "run", "handover");

    assert.notEqual(await h.ensure(), null);
    assert.deepEqual(stops, [[[4242], "SIGTERM"]], "the host this run did not start is stopped");
    assert.ok(existsSync(marker), "and its running sessions are handed over to this run's host");
    assert.equal(starts, 1, "the run starts its own");
    assert.notEqual(await h.ensure(), null);
    assert.equal(starts, 1, "its own host is kept");

    // Its own host misses one probe (load, a wake from sleep): kept, sessions and all.
    rmSync(marker);
    probes = 0;
    answers = false;
    setTimeout(() => (answers = true), 1000);
    assert.notEqual(await h.ensure(), null);
    assert.equal(probes, 2, "probed again after a miss");
    assert.equal(starts, 1, "a slow host is kept");
    assert.equal(stops.length, 1);

    // It stays silent: handed over (SIGUSR1, never SIGTERM, which ends the sessions), marker written.
    answers = false;
    h.hostPids = async () => [{ pid: process.pid, owned: true }];
    h.start = async () => {
      starts++;
      answers = true;
      h.own = process.pid;
    };
    assert.notEqual(await h.ensure(), null);
    assert.deepEqual(stops.at(-1), [[process.pid], "SIGUSR1"], "a silent own host hands its sessions over");
    assert.ok(existsSync(marker), "for the next host to adopt");
    assert.equal(starts, 2);

    await h.quit(true);
    assert.deepEqual(stops.at(-1), [[process.pid], "SIGUSR1"], "quitting to update hands the sessions over");
    h.own = process.pid;
    await h.quit();
    assert.deepEqual(stops.at(-1), [[process.pid], "SIGTERM"], "quitting ends the host and its sessions");
    const n = stops.length;
    await h.quit();
    assert.equal(stops.length, n, "a run without a host has nothing to end");
  } finally {
    if (prevHome === undefined) delete process.env.PENGUPOOL_HOME;
    else process.env.PENGUPOOL_HOME = prevHome;
    rmSync(dir, { recursive: true, force: true });
  }
});

/** The real Host, bundled by esbuild as the app is; any existing file stands in for the binary. */
async function bundledHost(dir: string): Promise<any> {
  const { buildSync } = await import("esbuild");
  const out = join(dir, "host.mjs");
  buildSync({ entryPoints: [join(import.meta.dirname, "host.ts")], bundle: true, platform: "node", format: "esm", outfile: out, logLevel: "silent" });
  const { Host } = (await import(out)) as typeof import("./host.ts");
  return new Host(process.execPath);
}
