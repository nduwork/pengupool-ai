// node --test src/update.test.ts (npm test): the install script is checked as text, never run.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

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
  });
  assert.ok(s.startsWith("#!/bin/sh\n"));
  assert.match(s, /^PID=4242$/m);
  assert.ok(s.includes(`BUNDLE='/Users/o'\\''neil/Applications/PenguPool AI.app'\n`));
  assert.ok(s.includes(`NEW='/Users/o'\\''neil/Applications/PenguPool AI.app.new'\n`));
  assert.ok(s.includes(`OLD='/Users/o'\\''neil/Applications/PenguPool AI.app.old'\n`));
  assert.ok(s.includes(`DMG='/Users/o'\\''neil/.pengupool-ai/v2/updates/v0.8.3/PenguPool-0.8.3-mac-universal.dmg'\n`));
  assert.ok(s.includes(`LOG='/Users/o'\\''neil/.pengupool-ai/v2/logs/update.log'\n`));
  assert.ok(s.includes(`HOST="$BUNDLE/Contents/Resources/bin/pengupool daemon"`));
  // The host is found by plain text, never as a pattern: a path may hold ( + . and the like.
  assert.ok(s.includes('index($0, ENVIRON["HOST"]) == 1')); // starts with the path, so a shell that mentions it is spared
  assert.ok(!/pgrep|pkill/.test(s));
  assert.ok(s.includes(`CLI="$HOME/.local/bin/pengupool-ai daemon"`) && s.includes(`index($0, ENVIRON["CLI"])`));
  assert.match(s, /^PATH=\/usr\/bin:\/bin:\/usr\/sbin:\/sbin\nexport PATH$/m);
  assert.ok(s.includes(`hdiutil detach "$MNT" -quiet >> "$LOG" 2>&1 || hdiutil detach "$MNT" -force -quiet`));
  assert.ok(s.includes(`[ -e "$NEW" ] && fail "could not remove $NEW"`));
  assert.ok(s.includes(`[ -e "$OLD" ] && fail "could not remove $OLD"`));
  assert.ok(s.includes(`mv "$OLD" "$BUNDLE" || log "could not restore $OLD"`));
  // fail() detaches (when mounted) and reopens the installed app.
  assert.match(s, /fail\(\) \{\n  log "failed: \$\*; reopening the installed app"\n  detach\n  rm -rf "\$NEW"\n  open "\$BUNDLE"/);
  assert.ok(s.includes(`hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$DMG"`));
  assert.ok(s.includes(`ditto "$MNT/PenguPool AI.app" "$NEW"`));
  // A signed app checks the new app's signature and team before copying it; an ad-hoc one skips the check.
  assert.ok(s.includes(`if [ -n "$TEAM" ] && [ "$TEAM" != "not set" ]; then`));
  assert.ok(s.includes(`codesign --verify --deep --strict "$MNT/PenguPool AI.app"`));
  assert.ok(s.includes(`[ "$NEWTEAM" = "$TEAM" ] || fail`));
  assert.ok(s.indexOf("codesign --verify") < s.indexOf("ditto"));
  // The installed app is moved aside only after the copy, and put back if the new one can't move in.
  assert.ok(s.indexOf("ditto") < s.indexOf(`mv "$BUNDLE" "$OLD"`));
  assert.ok(s.includes(`find "$NEW" -xattrname com.apple.quarantine -exec xattr -d -s com.apple.quarantine {} \\;`));
  assert.ok(s.trimEnd().endsWith(`rm -rf "$DIR"`));
});
