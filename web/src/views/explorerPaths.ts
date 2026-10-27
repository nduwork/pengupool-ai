// Path helpers of the Explorer: paths are relative to the session's working directory, with "/".

/** What a dragged Explorer row carries: the absolute path, for a session to type into its prompt. */
export const PATH_MIME = "application/x-pengupool-path";

export const parentDir = (p: string): string => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

export const joinRel = (dir: string, name: string): string => (dir === "" ? name : `${dir}/${name}`);

export const absPath = (cwd: string, rel: string): string => (rel === "" ? cwd : `${cwd.replace(/\/+$/, "")}/${rel}`);

/** A name that cannot be one file name (empty, a path, or a dot entry). */
export const badName = (n: string): boolean => n.trim() === "" || n.includes("/") || n === "." || n === "..";

/** C0 and C1 control characters, DEL included: in a file name they would end a bracketed paste and run as keystrokes. */
// eslint-disable-next-line no-control-regex
const CONTROL = /[\x00-\x1f\x7f-\x9f]/;
export const safePath = (p: string): boolean => p !== "" && !CONTROL.test(p);

/** The notice for a path that is not typed. */
export const UNSAFE_NOTICE = "Not typed: the file name has control characters.";

// A path dropped on a session is typed into its prompt once its terminal is live. The terminal pane
// is mounted only for the selected session, so a drop on another session's map tile queues the path,
// selects it, and its pane types it when the attach completes. A queued path expires, so it is never
// typed into a later attach.
export const QUEUE_TTL_MS = 5000;
const queue = new Map<string, { path: string; at: number }[]>();
const flushers = new Map<string, () => void>();

/** Queues a path for a session's prompt and nudges its terminal, if one is mounted. False: the path is refused (control characters). */
export function typePath(sessionId: string, path: string, now = Date.now()): boolean {
  if (!safePath(path)) return false;
  queue.set(sessionId, [...(queue.get(sessionId) ?? []), { path, at: now }]);
  flushers.get(sessionId)?.();
  return true;
}

/** The paths waiting for a session's prompt that have not expired, taken once; onExpired gets how many ran out. */
export function takePaths(sessionId: string, now = Date.now(), onExpired?: (n: number) => void): string[] {
  const q = queue.get(sessionId) ?? [];
  queue.delete(sessionId);
  const fresh = q.filter((x) => now - x.at <= QUEUE_TTL_MS);
  if (fresh.length < q.length) onExpired?.(q.length - fresh.length);
  return fresh.filter((x) => safePath(x.path)).map((x) => x.path);
}

/** A mounted terminal's way to be nudged; returns the function that withdraws it. */
export function onTypePath(sessionId: string, flush: () => void): () => void {
  flushers.set(sessionId, flush);
  return () => {
    if (flushers.get(sessionId) === flush) flushers.delete(sessionId);
  };
}
