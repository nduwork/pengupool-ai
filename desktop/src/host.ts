// The PenguPool host (the Go daemon) as the desktop app sees it. Each run of the app starts and owns
// its own host (`daemon --owner-pid`), from the bundled binary with the user's login-shell PATH, so
// the host is always the app's build. Quitting the app ends the host and its sessions; the next run
// asks to restore them. Quitting to install an update hands the sessions over instead: the host
// exits and leaves them running for the updated app's host to adopt.
import { execFile, spawn } from "node:child_process";
import { existsSync, mkdirSync, openSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { isStoreDaemon } from "./hostversion";

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

  /** The host this run started; 0 before it has one. */
  private own = 0;

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

  /** Starts this run's host, detached from the app's process group but ending with the app's run;
   *  its output goes to logs/daemon.log. */
  async start(): Promise<void> {
    const dir = storeDir();
    mkdirSync(join(dir, "logs"), { recursive: true });
    const log = openSync(join(dir, "logs", "daemon.log"), "a");
    const child = spawn(this.binary, ["daemon", "--store", dir, "--owner-pid", String(process.pid)], {
      env: await userEnv(),
      detached: true,
      stdio: ["ignore", log, log],
    });
    child.unref();
    this.own = child.pid ?? 0;
  }

  /**
   * This run's host: started on first use, waiting up to 20 s for its health check. One ensure runs
   * at a time; a second caller gets the first one's answer instead of starting a second host.
   * Before it starts a host it stops the store's others, so the app never talks to a host of another
   * build: its own host only when it misses two probes 3 s apart (a slow or waking host is kept), and
   * handing the sessions over (the next host adopts them), as for a host started by hand. A host of
   * another app run (--owner-pid) ends its own sessions.
   */
  ensure(): Promise<HostConnection | null> {
    this.pending ??= this.ensureOnce().finally(() => (this.pending = undefined));
    return this.pending;
  }

  private pending?: Promise<HostConnection | null>;

  private async ensureOnce(): Promise<HostConnection | null> {
    if (this.own > 0 && signal(this.own, 0)) {
      let found = await this.probe();
      if (found === null) {
        await new Promise((r) => setTimeout(r, 3000));
        found = await this.probe();
      }
      if (found !== null) return found;
    }
    if (!existsSync(this.binary)) throw new Error(`the bundled host is missing at ${this.binary}`);
    const hosts = await this.hostPids();
    const handOver = hosts.filter((h) => h.pid === this.own || !h.owned);
    if (handOver.length > 0) writeHandover(); // a wedged host cannot write it itself
    const own = hosts.filter((h) => h.pid === this.own).map((h) => h.pid);
    const others = hosts.filter((h) => h.pid !== this.own).map((h) => h.pid);
    // Its own host gets SIGUSR1, the hand-over: SIGTERM would end the sessions with the run.
    if ((await this.stop(own, "SIGUSR1")) + (await this.stop(others)) > 0) console.error("replaced a host; its sessions are handed over");
    this.own = 0;
    await this.start();
    for (let i = 0; i < 100; i++) {
      await new Promise((r) => setTimeout(r, 200));
      const c = await this.probe();
      if (c !== null) return c;
    }
    return null;
  }

  /**
   * Ends this run's host. Its sessions end with it and are recorded for the next run's restore
   * question; with handOver (quitting to install an update) the host exits and leaves them running
   * for the next host to adopt.
   */
  async quit(handOver = false): Promise<void> {
    if (this.own <= 0) return;
    const pid = this.own;
    this.own = 0;
    await this.stop([pid], handOver ? "SIGUSR1" : "SIGTERM", 30_000);
  }

  /** Stops hosts: sig, then SIGKILL for one still there after grace ms. Returns how many it stopped.
   *  A host whose code is gone (its disk image ejected) cannot handle a signal. */
  private async stop(pids: number[], sig: NodeJS.Signals = "SIGTERM", grace = 5000): Promise<number> {
    const hosts = pids.filter((pid) => signal(pid, sig));
    for (let i = 0; i < grace / 100 && hosts.some((pid) => signal(pid, 0)); i++) await new Promise((r) => setTimeout(r, 100));
    for (const pid of hosts) if (signal(pid, 0)) signal(pid, "SIGKILL");
    return hosts.length;
  }

  /** This store's running hosts: the lock file's pid and what pgrep finds for this binary's daemon,
   * each only when ps shows a PenguPool daemon of this store (a reused pid, a wrapper or an editor
   * whose command line merely names one are left out); owned when an app run owns it. */
  private async hostPids(): Promise<{ pid: number; owned: boolean }[]> {
    const pids = new Set<number>();
    const found = await run("/usr/bin/pgrep", ["-f", `${this.binary} daemon`], process.env, 3000);
    for (const pid of (found ?? "").split("\n").filter(Boolean)) pids.add(Number(pid));
    try {
      const locked = readFileSync(join(storeDir(), "daemon.lock"), "utf8").trim();
      if (/^\d+$/.test(locked)) pids.add(Number(locked));
    } catch {
      /* no lock file: nothing was ever started here */
    }
    const out: { pid: number; owned: boolean }[] = [];
    for (const pid of pids) {
      const cmd = (await run("/bin/ps", ["-o", "command=", "-p", String(pid)], process.env, 3000))?.trim();
      if (cmd !== undefined && isStoreDaemon(cmd, storeDir())) out.push({ pid, owned: cmd.includes(" --owner-pid ") });
    }
    return out;
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

  /** Starts a fresh host for this run; the sessions are handed over and keep running. */
  async restart(): Promise<HostConnection | null> {
    await this.quit(true);
    return this.ensure();
  }
}

/** Marks the running sessions as handed over: the next host adopts them (cmd/pengupool handoverFile). */
function writeHandover(): void {
  try {
    mkdirSync(join(storeDir(), "run"), { recursive: true });
    writeFileSync(join(storeDir(), "run", "handover"), new Date().toISOString().replace(/\.\d+Z$/, "Z"));
  } catch (e) {
    console.error("hand-over marker:", e);
  }
}

/** Sends sig to pid (0 only tests it is alive); false when there is no such process. */
function signal(pid: number, sig: NodeJS.Signals | 0): boolean {
  try {
    process.kill(pid, sig);
    return true;
  } catch {
    return false;
  }
}
