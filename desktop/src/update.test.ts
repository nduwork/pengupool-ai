// node --test src/update.test.ts (npm test): the install script is checked as text, and on a Mac also
// run against a stand-in bundle and store with stubbed hdiutil, codesign, open and mv.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { STOP_HOST_SH } from "./stophost.ts";
import { bundleOf, installScript, isUpdateImage, sq } from "./update.ts";

test("bundleOf finds the .app around the executable", () => {
  assert.equal(bundleOf("/Users/me/Applications/PenguPool AI.app/Contents/MacOS/PenguPool AI"), "/Users/me/Applications/PenguPool AI.app");
  assert.equal(bundleOf("/usr/local/bin/electron"), null);
  assert.equal(bundleOf("/x/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron"), "/x/node_modules/electron/dist/Electron.app");
});

test("isUpdateImage accepts only existing disk images under <store>/updates, links resolved", () => {
  // tmpdir is under /var, a link to /private/var on macOS: both sides are resolved.
  const store = join(mkdtempSync(join(tmpdir(), "pp-store-")), "v2");
  const files = ["updates/v0.8.3/PenguPool-0.8.3-mac-arm64.dmg", "updates/v0.8.3/run.sh", "evil.dmg", "updates-x/a.dmg"];
  for (const f of files) {
    mkdirSync(join(store, f, ".."), { recursive: true });
    writeFileSync(join(store, f), "x");
  }
  symlinkSync(join(store, "evil.dmg"), join(store, "updates", "link.dmg"));
  assert.ok(isUpdateImage(store, `${store}/updates/v0.8.3/PenguPool-0.8.3-mac-arm64.dmg`));
  assert.ok(!isUpdateImage(store, `${store}/updates/../evil.dmg`));
  assert.ok(!isUpdateImage(store, `${store}/updates/link.dmg`));
  assert.ok(!isUpdateImage(store, `${store}/updates/v0.8.3/run.sh`));
  assert.ok(!isUpdateImage(store, `${store}/updates-x/a.dmg`));
  assert.ok(!isUpdateImage(store, `${store}/updates/v0.8.4/missing.dmg`));
});

test("sq quotes for sh", () => {
  assert.equal(sq("it's"), `'it'\\''s'`);
});

test("installScript names every path, quoted", () => {
  const s = installScript({
    pid: 4242,
    bundle: "/Users/o'neil/Applications/PenguPool AI.app",
    dmg: "/Users/o'neil/.pengupool-ai/v2/updates/v0.8.3/PenguPool-0.8.3-mac-universal.dmg",
    log: "/Users/o'neil/.pengupool-ai/v2/logs/update.log",
    dir: "/tmp/pengupool-update-abc",
    store: "/Users/o'neil/.pengupool-ai/v2",
  });
  assert.ok(s.startsWith("#!/bin/sh\n"));
  assert.match(s, /^PID=4242$/m);
  assert.ok(s.includes(`BUNDLE='/Users/o'\\''neil/Applications/PenguPool AI.app'\n`));
  assert.ok(s.includes(`NEW='/Users/o'\\''neil/Applications/PenguPool AI.app.new'\n`));
  assert.ok(s.includes(`OLD='/Users/o'\\''neil/Applications/PenguPool AI.app.old'\n`));
  assert.ok(s.includes(`DMG='/Users/o'\\''neil/.pengupool-ai/v2/updates/v0.8.3/PenguPool-0.8.3-mac-universal.dmg'\n`));
  assert.ok(s.includes(`LOG='/Users/o'\\''neil/.pengupool-ai/v2/logs/update.log'\n`));
  assert.ok(s.includes(`STORE='/Users/o'\\''neil/.pengupool-ai/v2'\n`));
  // Only this store's host is stopped, found through its own lock (scripts/lib/stop-host.sh).
  assert.ok(s.includes(STOP_HOST_SH));
  assert.ok(s.includes(`stop_store_host "$STORE"`));
  assert.doesNotMatch(s, /pgrep|pkill|hostpids|HOST=|CLI=/);
  assert.ok(s.indexOf("ditto") < s.indexOf(`stop_store_host "$STORE"`));
  assert.ok(s.indexOf(`stop_store_host "$STORE"`) < s.indexOf(`mv "$BUNDLE" "$OLD"`));
  assert.match(s, /^PATH=\/usr\/bin:\/bin:\/usr\/sbin:\/sbin\nexport PATH$/m);
  assert.ok(s.includes(`hdiutil detach "$MNT" -quiet >> "$LOG" 2>&1 || hdiutil detach "$MNT" -force -quiet`));
  assert.ok(s.includes(`[ -e "$NEW" ] && fail "could not remove $NEW"`));
  assert.ok(s.includes(`[ -e "$OLD" ] && fail "could not remove $OLD"`));
  assert.ok(s.includes(`hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$DMG"`));
  assert.ok(s.includes(`ditto "$MNT/PenguPool AI.app" "$NEW"`));
  // A signed app checks the new app's signature and team before copying it; an ad-hoc one skips the check.
  assert.ok(s.includes(`if [ -n "$TEAM" ] && [ "$TEAM" != "not set" ]; then`));
  // The team is anchored to Apple's Developer ID chain, not read from a field an ad-hoc signer can set.
  assert.ok(s.includes(`codesign --verify --deep --strict -R="anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \\"$TEAM\\"" "$MNT/PenguPool AI.app"`));
  assert.ok(s.indexOf("codesign --verify") < s.indexOf("ditto"));
  // The installed app is moved aside only after the copy, and put back if the new one can't move in.
  assert.ok(s.indexOf("ditto") < s.indexOf(`mv "$BUNDLE" "$OLD"`));
  assert.ok(s.includes(`find "$NEW" -xattrname com.apple.quarantine -exec xattr -d -s com.apple.quarantine {} \;`));
  // One way out: finish() reopens and cleans up; fail() only logs and exits.
  assert.ok(s.includes("trap finish EXIT"));
  assert.ok(s.includes(`fail() { log "failed: $*"; exit 1; }`));
  const finish = s.slice(s.indexOf("finish() {"), s.indexOf("\n}\n", s.indexOf("finish() {")));
  assert.equal(s.split(`open "$APP"`).length - 1, 2); // the try and the retry, both inside finish
  assert.ok(finish.includes(`open "$APP" || { sleep 2; open "$APP"; }`));
  assert.ok(!s.includes(`open "$BUNDLE"`));
  assert.ok(s.indexOf(`mv "$NEW" "$BUNDLE"`) < s.lastIndexOf("STATUS=0"));
  assert.ok(s.trimEnd().endsWith("STATUS=0"));
});

test("the update script carries stop-host.sh byte for byte, with nothing a template literal changes", () => {
  assert.equal(STOP_HOST_SH, readFileSync(new URL("../../scripts/lib/stop-host.sh", import.meta.url), "utf8"));
  assert.ok(!STOP_HOST_SH.includes("${") && !STOP_HOST_SH.includes("`") && !STOP_HOST_SH.includes("\\"));
});

// The script run for real against a stand-in bundle, store and tools (hdiutil, codesign, open, mv).
const mac = process.platform === "darwin";

function fixture() {
  const t = realpathSync(mkdtempSync(join(tmpdir(), "pp-upd-")));
  const bundle = join(t, "Applications", "PenguPool AI.app");
  const store = join(t, "store");
  const put = (f: string, body = "x") => {
    mkdirSync(dirname(f), { recursive: true });
    writeFileSync(f, body);
  };
  put(join(bundle, "v"), "old");
  mkdirSync(`${bundle}.old`, { recursive: true }); // a stale one an earlier update left
  put(join(t, "newapp", "PenguPool AI.app", "v"), "new");
  put(join(store, "keep.json"), "keep");
  put(join(store, "backups", "b.json"), "backup");
  const dmg = join(store, "updates", "v9.9.9", "PenguPool-AI-9.9.9-mac-arm64.dmg");
  put(dmg);
  put(join(store, "updates", "v0.0.1", "x.dmg"));
  put(join(store, "updates", ".partial"));
  symlinkSync("../keep.json", join(store, "updates", "stray"));
  const dir = join(t, "tmp", "pengupool-update-run");
  mkdirSync(dir, { recursive: true });
  mkdirSync(join(t, "tmp", "pengupool-update-stale", "mnt"), { recursive: true });
  const bin = join(t, "bin");
  const stub = (name: string, body: string) => {
    put(join(bin, name), `#!/bin/sh\nT=${sq(t)}\n${body}\n`);
    chmodSync(join(bin, name), 0o755);
  };
  stub(
    "hdiutil",
    `case "$1" in
attach) [ -e "$T/fail-attach" ] && exit 1
  while [ $# -gt 0 ]; do [ "$1" = -mountpoint ] && m=$2; shift; done
  mkdir -p "$m" && cp -R "$T/newapp/PenguPool AI.app" "$m/" ;;
detach) rm -rf "$2"/* ;;
esac
exit 0`,
  );
  stub("codesign", "exit 0");
  stub("open", `echo "$*" >> "$T/opened"`);
  stub(
    "mv",
    `case "$1" in
*.app.new) [ -e "$T/fail-swap" ] && exit 1 ;;
*.app.old) [ -e "$T/fail-restore" ] && exit 1 ;;
esac
exec /bin/mv "$@"`,
  );
  const ended = spawnSync("/usr/bin/true").pid ?? 999999;
  const script = (pid = ended) => {
    const f = join(dir, "update.sh");
    writeFileSync(f, installScript({ pid, bundle, dmg, log: join(store, "logs", "update.log"), dir, store, path: `${bin}:/usr/bin:/bin:/usr/sbin:/sbin` }), { mode: 0o700 });
    return f;
  };
  const opened = () => (existsSync(join(t, "opened")) ? readFileSync(join(t, "opened"), "utf8").trim().split("\n") : []);
  const v = (b = bundle) => readFileSync(join(b, "v"), "utf8");
  return { t, bundle, store, dmg, dir, script, opened, v };
}

test("a good update leaves the new app, opened once, and nothing of the updater's", { skip: !mac }, () => {
  const f = fixture();
  const r = spawnSync("/bin/sh", [f.script()]);
  assert.equal(r.status, 0, readFileSync(join(f.store, "logs", "update.log"), "utf8"));
  assert.equal(f.v(), "new");
  assert.deepEqual(f.opened(), [f.bundle]);
  assert.ok(!existsSync(`${f.bundle}.old`) && !existsSync(`${f.bundle}.new`));
  assert.ok(!existsSync(f.dir) && !existsSync(join(f.t, "tmp", "pengupool-update-stale")));
  assert.deepEqual(readdirSync(join(f.store, "updates")), []);
  assert.equal(readFileSync(join(f.store, "keep.json"), "utf8"), "keep");
  assert.equal(readFileSync(join(f.store, "backups", "b.json"), "utf8"), "backup");
  assert.equal(statSync(join(f.store, "logs", "update.log")).mode & 0o777, 0o600);
});

test("an image that won't attach reopens the installed app and keeps the download", { skip: !mac }, () => {
  const f = fixture();
  writeFileSync(join(f.t, "fail-attach"), "");
  assert.equal(spawnSync("/bin/sh", [f.script()]).status, 1);
  assert.equal(f.v(), "old");
  assert.deepEqual(f.opened(), [f.bundle]);
  assert.ok(!existsSync(f.dir) && existsSync(f.dmg));
});

test("a failed swap puts the installed app back and reopens it", { skip: !mac }, () => {
  const f = fixture();
  writeFileSync(join(f.t, "fail-swap"), "");
  assert.equal(spawnSync("/bin/sh", [f.script()]).status, 1);
  assert.equal(f.v(), "old");
  assert.ok(!existsSync(`${f.bundle}.old`) && !existsSync(`${f.bundle}.new`));
  assert.deepEqual(f.opened(), [f.bundle]);
  assert.ok(existsSync(f.dmg));
});

test("a failed swap whose restore fails too opens the app left at .app.old", { skip: !mac }, () => {
  const f = fixture();
  writeFileSync(join(f.t, "fail-swap"), "");
  writeFileSync(join(f.t, "fail-restore"), "");
  assert.equal(spawnSync("/bin/sh", [f.script()]).status, 1);
  assert.deepEqual(f.opened(), [`${f.bundle}.old`]);
  assert.equal(f.v(`${f.bundle}.old`), "old");
});

test("a script stopped by a signal still reopens the app and cleans up", { skip: !mac }, async () => {
  const f = fixture();
  const app = spawn("/bin/sleep", ["30"]);
  try {
    const run = spawn("/bin/sh", [f.script(app.pid)]);
    const log = join(f.store, "logs", "update.log");
    for (let i = 0; i < 50 && !(existsSync(log) && readFileSync(log, "utf8").includes("updating")); i++) await new Promise((r) => setTimeout(r, 100));
    const done = new Promise((r) => run.on("exit", r));
    run.kill("SIGTERM");
    await done;
    assert.deepEqual(f.opened(), [f.bundle]);
    assert.ok(!existsSync(f.dir));
  } finally {
    app.kill();
  }
});
