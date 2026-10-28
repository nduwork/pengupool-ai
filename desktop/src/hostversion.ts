// The running host's build, as the app checks it: the version line the daemon wrote at startup
// (cmd/pengupool writes run/host.version), or null when the daemon predates that file. Plain
// functions only, so node --test runs them.
import { readFileSync } from "node:fs";
import { join } from "node:path";

/** The running host's build: the version line it wrote at startup, or null when it predates that file. */
export function readHostVersion(store: string): string | null {
  try {
    const v = readFileSync(join(store, "run", "host.version"), "utf8").trim();
    return v === "" ? null : v;
  } catch {
    return null; // predates the version file, or nothing ever started here
  }
}

/** Whether the running host is not this build: unknown (predates the version file) or a different version line. */
export function hostOutdated(running: string | null, bundled: string): boolean {
  return running !== bundled;
}

/** Whether a ps command line is a PenguPool daemon of store: a pengupool binary (by absolute path or
 *  name, not inside another command) running `daemon`, with this --store or none (the default store). */
export function isStoreDaemon(cmd: string, store: string): boolean {
  if (!/^(\/[^'"]*\/)?pengupool(\.exe)? daemon( |$)/.test(cmd)) return false;
  const i = cmd.indexOf(" --store ");
  if (i < 0) return true;
  const rest = cmd.slice(i + " --store ".length);
  return rest === store || rest.startsWith(store + " ");
}
