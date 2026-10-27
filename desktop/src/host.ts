// The pengu owner API as the desktop app sees it: find the running one for
// this store, or start the bundled binary with the user's login-shell PATH.
// The server is never tied to the app's lifetime: quitting the app leaves it
// running. Auth is a same-machine token in the store (0600), shared with the
// CLI's owner-serve fallback.
import { execFile, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, openSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

export interface OwnerConnection {
  url: string;
  token: string;
}

export function storeDir(): string {
  const home = process.env.PENGUPOOL_AI_HOME;
  return home !== undefined && home !== "" ? home : join(homedir(), ".pengupool-ai");
}

export function tokenFile(): string {
  return join(storeDir(), "owner-token");
}

/** The same-machine token, created once and shared with the CLI. */
export function loadOrCreateToken(): string {
  mkdirSync(storeDir(), { recursive: true });
  try {
    const t = readFileSync(tokenFile(), "utf8").trim();
    if (t !== "") return t;
  } catch {
    /* create below */
  }
  const t = randomBytes(16).toString("hex");
  writeFileSync(tokenFile(), t + "\n", { mode: 0o600 });
  return t;
}

const run = promisify(execFile);

let loginEnv: NodeJS.ProcessEnv | undefined;

/** The user's login-shell environment, so tmux, claude and muse resolve as in their terminal. */
export async function userEnv(): Promise<NodeJS.ProcessEnv> {
  if (loginEnv !== undefined) return loginEnv;
  const env: NodeJS.ProcessEnv = { ...process.env };
  const shell = env.SHELL ?? "/bin/zsh";
  try {
    const { stdout } = await run(shell, ["-l", "-i", "-c", 'printf "__PATH__%s" "$PATH"'], {
      env,
      timeout: 20000,
    });
    const path = stdout.split("__PATH__").pop();
    if (path !== undefined && path !== "") {
      env.PATH = path;
      loginEnv = env;
    }
  } catch {
    /* keep the inherited PATH; a later Restart retries */
  }
  // Never leak Electron's own variables into agents.
  delete env.ELECTRON_RUN_AS_NODE;
  delete env.ELECTRON_NO_ATTACH_CONSOLE;
  return env;
}

async function probe(url: string, token: string): Promise<boolean> {
  try {
    const res = await fetch(`${url}/lines`, {
      headers: { Authorization: `Bearer ${token}` },
      signal: AbortSignal.timeout(1500),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export class Host {
  constructor(
    private readonly binary: string,
    private readonly port = 8471,
  ) {}

  get cli(): string {
    return this.binary;
  }

  url(): string {
    return `http://127.0.0.1:${this.port}`;
  }

  /** Link the bundled CLI into ~/.local/bin so `pengu` works from a DMG install too. */
  linkCli(): void {
    try {
      const binDir = join(homedir(), ".local", "bin");
      mkdirSync(binDir, { recursive: true });
      const link = join(binDir, "pengu");
      try {
        symlinkSync(this.binary, link);
      } catch {
        /* already there (ours or the user's); leave it */
      }
    } catch {
      /* the app still works; only the CLI link is missing */
    }
  }

  /** Finds the server or starts it, waiting up to 20 s for its health check. */
  async ensure(): Promise<OwnerConnection> {
    const token = loadOrCreateToken();
    const url = this.url();
    if (await probe(url, token)) return { url, token };
    if (!existsSync(this.binary)) throw new Error(`the bundled host is missing at ${this.binary}`);
    this.linkCli();
    const dir = storeDir();
    mkdirSync(join(dir, "logs"), { recursive: true });
    const log = openSync(join(dir, "logs", "owner-serve.log"), "a");
    const env = await userEnv();
    env.PENGUPOOL_OWNER_TOKEN = token;
    env.PENGUPOOL_AI_HOME = dir;
    const child = spawn(this.binary, ["owner-serve", "--bind", "127.0.0.1", "--port", String(this.port)], {
      env,
      detached: true,
      stdio: ["ignore", log, log],
    });
    child.unref();
    for (let i = 0; i < 100; i++) {
      await new Promise((r) => setTimeout(r, 200));
      if (await probe(url, token)) return { url, token };
    }
    throw new Error("the owner API did not answer after 20 s; see logs/owner-serve.log");
  }

  /** Stops the server and starts it fresh. */
  async restart(): Promise<OwnerConnection> {
    try {
      const { stdout } = await run("/usr/bin/pgrep", ["-f", `${this.binary} owner-serve`]);
      for (const pid of stdout.split("\n").filter(Boolean)) {
        try {
          process.kill(Number(pid), "SIGTERM");
        } catch {
          /* already gone */
        }
      }
    } catch {
      /* not running */
    }
    const token = loadOrCreateToken();
    for (let i = 0; i < 50 && (await probe(this.url(), token)); i++) {
      await new Promise((r) => setTimeout(r, 100));
    }
    return this.ensure();
  }
}
