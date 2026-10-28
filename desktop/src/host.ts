// The PenguPool host (the Go daemon) as the desktop app sees it: find the running one for this
// store, or start the bundled binary with the user's login-shell PATH. The host is never tied to
// the app's lifetime: quitting the app leaves it, and every session, running.
import { execFile, spawn } from "node:child_process";
import { existsSync, mkdirSync, openSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export interface HostConnection {
  url: string;
  token: string;
}

/** An environment the app can work in: this machine, or an SSH host set up in Settings. */
export interface Environment {
  id: string;
  name: string;
  kind: string; // local | ssh
  address?: string;
  status: string;
  reason?: string;
}

export function storeDir(): string {
  const home = process.env.PENGUPOOL_HOME;
  return home !== undefined && home !== "" ? join(home, "v2") : join(homedir(), ".pengupool-ai", "v2");
}

function run(file: string, args: string[], env: NodeJS.ProcessEnv, timeoutMs: number): Promise<string | null> {
  return new Promise((resolve) => {
    execFile(file, args, { env, timeout: timeoutMs }, (err, stdout) => resolve(err === null ? stdout.trim() : null));
  });
}

let loginEnv: NodeJS.ProcessEnv | undefined;

/** The user's login-shell environment, so claude, omp, git and ssh resolve as in their terminal. */
export async function userEnv(): Promise<NodeJS.ProcessEnv> {
  if (loginEnv !== undefined) return loginEnv;
  const env: NodeJS.ProcessEnv = { ...process.env };
  const shell = env.SHELL ?? "/bin/zsh";
  // A cold first launch (after an update, say) can be slow to run the shell's startup files.
  const out = await run(shell, ["-l", "-i", "-c", 'printf "__PATH__%s" "$PATH"'], env, 20000);
  const path = out?.split("__PATH__").pop();
  const found = path !== undefined && path !== "";
  if (found) env.PATH = path;
  // Never leak Electron's own variables into agents.
  delete env.ELECTRON_RUN_AS_NODE;
  delete env.ELECTRON_NO_ATTACH_CONSOLE;
  env.PENGUPOOL_APP = "desktop";
  // A failed lookup is tried again at the next start (Restart host), not kept for the app's life;
  // meanwhile the host adds the usual install folders to the bare PATH itself.
  if (found) loginEnv = env;
  return env;
}

export class Host {
  constructor(private readonly binary: string) {}

  get cli(): string {
    return this.binary;
  }

  /** The running host's address and token, if it answers its health check. */
  async probe(): Promise<HostConnection | null> {
    const out = await run(this.binary, ["web", "--store", storeDir()], process.env, 3000);
    if (out === null) return null;
    let u: URL;
    try {
      u = new URL(out);
    } catch {
      return null;
    }
    const token = new URLSearchParams(u.hash.slice(1)).get("token") ?? "";
    try {
      const res = await fetch(`${u.origin}/api/healthz`, { headers: { "X-PenguPool-Token": token }, signal: AbortSignal.timeout(1500) });
      // A host of another store can answer on the same port; only ours accepts our token.
      if (!res.ok || ((await res.json()) as { owner?: boolean }).owner !== true) return null;
    } catch {
      return null;
    }
    return { url: u.origin, token };
  }

  /** Starts the host detached from the app; its output goes to logs/daemon.log. */
  async start(): Promise<void> {
    const dir = storeDir();
    mkdirSync(join(dir, "logs"), { recursive: true });
    const log = openSync(join(dir, "logs", "daemon.log"), "a");
    const child = spawn(this.binary, ["daemon", "--store", dir], {
      env: await userEnv(),
      detached: true,
      stdio: ["ignore", log, log],
    });
    child.unref();
  }

  /** Finds the host or starts it, waiting up to 20 s for its health check. */
  async ensure(): Promise<HostConnection | null> {
    const found = await this.probe();
    if (found !== null) return found;
    if (!existsSync(this.binary)) throw new Error(`the bundled host is missing at ${this.binary}`);
    await this.start();
    for (let i = 0; i < 100; i++) {
      await new Promise((r) => setTimeout(r, 200));
      const c = await this.probe();
      if (c !== null) return c;
    }
    return null;
  }

  /** Runs a pool request on this machine's host through the CLI; it rejects with the host's message. */
  rpc<T>(type: string, params: unknown): Promise<T> {
    return new Promise((resolve, reject) => {
      execFile(this.binary, ["rpc", "--store", storeDir(), type, JSON.stringify(params)], { env: process.env, timeout: 6 * 60_000 }, (err, stdout, stderr) => {
        if (err !== null) {
          reject(new Error(stderr.trim().replace(/^pengupool: /, "") || err.message));
          return;
        }
        try {
          resolve(JSON.parse(stdout) as T);
        } catch {
          reject(new Error(`unexpected answer to ${type}`));
        }
      });
    });
  }

  /** Stops the host (sessions keep running and are adopted again) and starts it fresh. */
  async restart(): Promise<HostConnection | null> {
    const pids = await run("/usr/bin/pgrep", ["-f", `${this.binary} daemon --store ${storeDir()}`], process.env, 3000);
    for (const pid of (pids ?? "").split("\n").filter(Boolean)) {
      try {
        process.kill(Number(pid), "SIGTERM");
      } catch {
        /* already gone */
      }
    }
    for (let i = 0; i < 50 && (await this.probe()) !== null; i++) await new Promise((r) => setTimeout(r, 100));
    return this.ensure();
  }
}
