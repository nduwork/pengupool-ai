// Which processes are this store's PenguPool hosts, as the app checks before it starts its own.
// Plain functions only, so node --test runs them.

/** Whether a ps command line is a PenguPool daemon of store: a pengupool binary (by absolute path or
 *  name, not inside another command) running `daemon`, with this --store or none (the default store). */
export function isStoreDaemon(cmd: string, store: string): boolean {
  if (!/^(\/[^'"]*\/)?pengupool(\.exe)? daemon( |$)/.test(cmd)) return false;
  const i = cmd.indexOf(" --store ");
  if (i < 0) return true;
  const rest = cmd.slice(i + " --store ".length);
  return rest === store || rest.startsWith(store + " ");
}
