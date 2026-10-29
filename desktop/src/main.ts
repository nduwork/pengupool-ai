// PenguPool desktop: main process. It supervises the Go host, serves the UI from the app bundle on
// the app:// scheme, and gives the renderer a small native bridge (folder picker, notifications,
// dock badge, external links). The renderer talks to the host directly over its WebSocket.
//
// The app works in one environment at a time: this machine's host, or the host on one SSH host,
// reached through a port this machine's host forwards over SSH. The UI is then entirely that host's.
import { BrowserWindow, Menu, Notification, app, dialog, ipcMain, net, protocol, screen, shell } from "electron";
import { execFile, spawn } from "node:child_process";
import { accessSync, constants, existsSync, lstatSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, extname, join, normalize } from "node:path";
import { pathToFileURL } from "node:url";

import { Host, storeDir, userEnv, type Environment, type HostConnection } from "./host";
import { bundleOf, installScript, isUpdateImage } from "./update";

app.setName("PenguPool AI");

const packaged = app.isPackaged;
const rendererRoot = packaged ? join(process.resourcesPath, "renderer") : join(__dirname, "..", "..", "internal", "webapp", "dist");
const host = new Host(packaged ? join(process.resourcesPath, "bin", "pengupool") : join(__dirname, "..", "..", "bin", "pengupool"));

protocol.registerSchemesAsPrivileged([
  { scheme: "app", privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: true, stream: true } },
]);

let win: BrowserWindow | null = null;
let current: HostConnection | null = null;

// ---- environment --------------------------------------------------------------------------------

/** The SSH host the app works in, or null for this machine, and its name for the window. */
let envId: string | null = null;
let envName = "";
const envPath = (): string => join(app.getPath("userData"), "environment.json");

function loadEnv(): void {
  try {
    const e = JSON.parse(readFileSync(envPath(), "utf8")) as { id?: string; name?: string };
    envId = typeof e.id === "string" && e.id !== "" ? e.id : null;
    envName = typeof e.name === "string" ? e.name : "";
  } catch {
    envId = null;
  }
}

function saveEnv(): void {
  try {
    writeFileSync(envPath(), JSON.stringify({ id: envId ?? "", name: envName }));
  } catch {
    /* best effort */
  }
}

/** The connection for the current environment: this machine's host, or the SSH host's through its forward. */
async function connect(): Promise<HostConnection | null> {
  const local = await host.ensure();
  if (envId === null || local === null) return local;
  try {
    return await host.rpc<HostConnection>("node.connect", { id: envId });
  } catch (e) {
    const failed = envId;
    envId = null;
    saveEnv();
    void dialog.showMessageBox({
      type: "warning",
      message: "Could not switch to the SSH host",
      detail: `${String(e instanceof Error ? e.message : e)}\n\nPenguPool is showing this machine instead. Fix the host in Settings → SSH hosts, then pick it again.`,
    });
    console.error(`environment ${failed}:`, e);
    return local;
  }
}

// ---- first launch: pengupool doctor -------------------------------------------------------------

/** On the first launch of an install, runs `pengupool doctor`. Only its warnings are shown; if Claude's settings hold
 * PenguPool v1 hooks the owner can have them removed (the CLI keeps what it removes in PenguPool's record). Asked once per machine and user. */
async function firstRunDoctor(): Promise<void> {
  const marker = join(app.getPath("userData"), "doctor-ran");
  if (existsSync(marker)) return;
  const env = await userEnv();
  const run = (args: string[]): Promise<{ out: string; ok: boolean }> =>
    new Promise((resolve) => execFile(host.cli, ["doctor", ...args], { env, timeout: 90_000 }, (e, out) => resolve({ out: String(out ?? ""), ok: e === null })));
  const first = await run([]);
  if (first.ok) {
    try {
      writeFileSync(marker, new Date().toISOString()); // a timed-out run asks again next launch
    } catch {
      /* it asks again next launch */
    }
  }
  const warns = first.out.split("\n").filter((l) => l.startsWith("warn"));
  if (warns.length === 0) return;
  const v1 = warns.some((l) => l.includes("holds PenguPool v1 entries"));
  const r = await dialog.showMessageBox({
    type: "warning",
    message: "PenguPool found things to fix on this machine",
    detail: `${warns.join("\n")}\n\nRun \`pengupool doctor\` in a terminal to see this again.`,
    buttons: v1 ? ["Clean Claude's settings", "Later"] : ["OK"],
    defaultId: 0,
  });
  if (v1 && r.response === 0) {
    const fixed = await run(["--fix", "--yes"]);
    const done = fixed.out.split("\n").filter((l) => l.startsWith("done"));
    void dialog.showMessageBox(
      done.length > 0
        ? { type: "info", message: "Claude's settings cleaned", detail: done.join("\n") }
        : { type: "warning", message: "Claude's settings were not changed", detail: fixed.out },
    );
  }
}

// ---- the run's sessions ------------------------------------------------------------------------

/** Quitting to install an update: the host leaves its sessions running for the updated app. */
let handingOver = false;

type Restorable = { id: string; name: string; harness: string; agent: boolean };

/** At start, offers the sessions the last run ended with (a quit, crash or power loss). */
async function askRestore(): Promise<void> {
  if ((await host.ensure()) === null) return;
  const last = await host.rpc<Restorable[] | null>("run.restorable", {}).catch(() => null);
  if (last === null || last.length === 0) return;
  const agents = last.filter((s) => s.agent);
  const terminals = last.length - agents.length;
  const r = await dialog.showMessageBox({
    type: "question",
    message: `Restore ${last.length === 1 ? "the session" : `the ${last.length} sessions`} from your last run?`,
    detail:
      last.map((s) => `• ${s.name}${s.agent ? "" : " (terminal)"}`).join("\n") +
      (terminals > 0 ? "\n\nA terminal cannot be restored; agents resume their conversations." : "\n\nAgents resume their conversations."),
    buttons: agents.length > 0 ? ["Restore all", "Don't restore"] : ["OK"],
    defaultId: 0,
    cancelId: agents.length > 0 ? 1 : 0,
  });
  await host.rpc(agents.length > 0 && r.response === 0 ? "run.restore" : "run.dismiss", {}).catch((e: unknown) => console.error("restore:", e));
}

// ---- window state -------------------------------------------------------------------------------

interface WinState {
  x?: number;
  y?: number;
  width: number;
  height: number;
  maximized?: boolean;
}
const statePath = (): string => join(app.getPath("userData"), "window-state.json");

function loadState(): WinState {
  try {
    const s = JSON.parse(readFileSync(statePath(), "utf8")) as WinState;
    const visible = screen.getAllDisplays().some((d) => s.x !== undefined && s.y !== undefined && s.x >= d.bounds.x - 50 && s.y >= d.bounds.y - 50 && s.x < d.bounds.x + d.bounds.width && s.y < d.bounds.y + d.bounds.height);
    return visible ? s : { width: s.width, height: s.height };
  } catch {
    return { width: 1480, height: 940 };
  }
}

function saveState(w: BrowserWindow): void {
  const b = w.getNormalBounds();
  try {
    writeFileSync(statePath(), JSON.stringify({ ...b, maximized: w.isMaximized() }));
  } catch {
    /* best effort */
  }
}

function createWindow(): void {
  const s = loadState();
  win = new BrowserWindow({
    ...s,
    minWidth: 960,
    minHeight: 620,
    show: false,
    title: "PenguPool AI",
    titleBarStyle: "hiddenInset",
    trafficLightPosition: { x: 16, y: 15 },
    backgroundColor: "#111318",
    webPreferences: {
      preload: join(__dirname, "preload.js"),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      spellcheck: false,
      plugins: true, // Chromium's PDF viewer, for the file preview
    },
  });
  if (s.maximized === true) win.maximize();
  win.once("ready-to-show", () => win?.show());
  win.on("close", () => win !== null && saveState(win));
  win.on("closed", () => {
    win = null;
  });
  // Only the app's own pages load in the window; links open in the user's browser.
  win.webContents.on("will-navigate", (e, url) => {
    if (!url.startsWith("app://pengupool/")) {
      e.preventDefault();
      openExternal(url);
    }
  });
  win.webContents.setWindowOpenHandler(({ url }) => {
    openExternal(url);
    return { action: "deny" };
  });
  void win.loadURL("app://pengupool/index.html");
}

function openExternal(url: string): void {
  if (/^https?:\/\//.test(url)) void shell.openExternal(url);
}

// ---- app:// serves the renderer bundle ---------------------------------------------------------

function serveRenderer(): void {
  protocol.handle("app", (req) => {
    const u = new URL(req.url);
    const rel = normalize(decodeURIComponent(u.pathname)).replace(/^([/\\])+/, "");
    const file = join(rendererRoot, rel === "" ? "index.html" : rel);
    if (!file.startsWith(rendererRoot) || !existsSync(file)) return new Response("not found", { status: 404 });
    return net.fetch(pathToFileURL(file).toString());
  });
}

// ---- IPC bridge ---------------------------------------------------------------------------------

function ipc(): void {
  ipcMain.handle("host:connection", async () => {
    try {
      current = await connect();
    } catch (e) {
      dialog.showErrorBox("PenguPool could not start its host", String(e));
      current = null;
    }
    return current;
  });
  ipcMain.handle("host:restart", async () => {
    await host.restart();
    current = await connect();
    win?.webContents.reload();
    return current;
  });
  ipcMain.handle("env:list", async () => {
    let items: Environment[] = [];
    try {
      if ((await host.ensure()) !== null) items = await host.rpc<Environment[]>("node.list", {});
    } catch {
      /* the switcher still offers this machine */
    }
    return { current: envId, items };
  });
  ipcMain.handle("env:switch", (_e, id: unknown, name: unknown) => switchTo(typeof id === "string" && id !== "" ? id : null, String(name ?? "")));
  // Read once by the preload: the window reloads on every switch.
  ipcMain.on("env:current", (e) => {
    e.returnValue = envId === null ? null : { id: envId, name: envName };
  });
  ipcMain.handle("dialog:pickFolder", async () => {
    // A folder on this machine means nothing to an SSH host; the renderer asks for a path instead.
    if (win === null || envId !== null) return null;
    const r = await dialog.showOpenDialog(win, { properties: ["openDirectory", "createDirectory"], buttonLabel: "Choose" });
    return r.canceled ? null : (r.filePaths[0] ?? null);
  });
  ipcMain.on("shell:openExternal", (_e, url: string) => openExternal(String(url)));
  ipcMain.on("shell:reveal", (_e, path: string) => {
    if (envId !== null || typeof path !== "string" || !path.startsWith("/")) return;
    // Only a real folder (not a link) whose name has no extension is opened (an extension marks a bundle:
    // .app, .prefPane, .saver, .pkg...); anything else is shown selected in its folder, so nothing is launched from here.
    let plainFolder = false;
    try {
      plainFolder = lstatSync(path).isDirectory() && extname(basename(path)) === "";
    } catch {
      /* gone: showItemInFolder opens its folder */
    }
    if (plainFolder) void shell.openPath(path);
    else shell.showItemInFolder(path);
  });
  ipcMain.on("app:notify", (_e, title: string, body: string) => {
    // Only when the window is not focused: in the app, the bell and banner already show it.
    if (win?.isFocused() === true || !Notification.isSupported()) return;
    const n = new Notification({ title: String(title), body: String(body) });
    n.on("click", () => {
      win?.show();
      win?.focus();
    });
    n.show();
  });
  ipcMain.on("app:badge", (_e, count: number) => {
    if (process.platform === "darwin") app.dock?.setBadge(count > 0 ? String(count) : "");
  });
  ipcMain.handle("update:install", (_e, path: unknown) => installUpdate(String(path)));
}

/** Replaces this app with the disk image the host downloaded, and restarts into it (./update.ts). */
function installUpdate(dmg: string): void {
  const bundle = bundleOf(process.execPath);
  if (!packaged || bundle === null) throw new Error("only an installed PenguPool AI.app can update itself");
  if (process.arch !== "arm64") throw new Error("PenguPool now runs on Apple Silicon Macs only; this Mac can't install the update. Keep using this version.");
  if (envId !== null) throw new Error("switch to This machine to update");
  if (!isUpdateImage(storeDir(), dmg)) throw new Error(`not a downloaded update: ${dmg}`);
  try {
    accessSync(dirname(bundle), constants.W_OK);
  } catch {
    throw new Error(`PenguPool can't replace itself in ${dirname(bundle)}; move it to a folder you can write to or update with the disk image`);
  }
  const dir = mkdtempSync(join(tmpdir(), "pengupool-update-"));
  const script = join(dir, "update.sh");
  writeFileSync(script, installScript({ pid: process.pid, bundle, dmg, log: join(storeDir(), "logs", "update.log"), dir }), { mode: 0o700 });
  const env = { ...process.env };
  delete env.ELECTRON_RUN_AS_NODE;
  spawn("/bin/sh", [script], { detached: true, stdio: "ignore", env }).unref();
  handingOver = true; // the updated app's host adopts this run's sessions
  app.quit();
}

function switchTo(id: string | null, name = ""): void {
  envId = id;
  envName = id === null ? "" : name;
  saveEnv();
  current = null;
  win?.webContents.reload();
}

// ---- menu ---------------------------------------------------------------------------------------

function menu(): void {
  const tmpl: Electron.MenuItemConstructorOptions[] = [
    { role: "appMenu" },
    { role: "editMenu" },
    {
      label: "View",
      submenu: [
        { role: "reload" },
        { role: "toggleDevTools" },
        { type: "separator" },
        { role: "resetZoom" },
        { role: "zoomIn" },
        { role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    {
      label: "Host",
      submenu: [
        { label: "Restart Host", click: () => void host.restart().then(() => win?.webContents.reload()) },
        { label: "Switch to This machine", click: () => switchTo(null) },
        { label: "Show Data Folder", click: () => void shell.openPath(storeDir()) },
        { label: "Show Host Log", click: () => void shell.openPath(join(storeDir(), "logs", "daemon.log")) },
        {
          label: "Open in Browser",
          click: async () => {
            const c = current ?? (await host.probe());
            if (c !== null) void shell.openExternal(`${c.url}/#token=${c.token}`);
          },
        },
      ],
    },
    { role: "windowMenu" },
  ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(tmpl));
}

// ---- lifecycle ----------------------------------------------------------------------------------

const single = app.requestSingleInstanceLock();
if (!single) {
  app.quit();
} else {
  app.on("second-instance", () => {
    if (win !== null) {
      if (win.isMinimized()) win.restore();
      win.focus();
    }
  });
  void app.whenReady().then(() => {
    loadEnv();
    serveRenderer();
    ipc();
    menu();
    createWindow();
    void firstRunDoctor();
    void askRestore();
    app.on("activate", () => {
      if (BrowserWindow.getAllWindows().length === 0) createWindow();
    });
  });
  // On macOS the app stays in the Dock like other apps; the run ends when the app quits.
  app.on("window-all-closed", () => {
    if (process.platform !== "darwin") app.quit();
  });
  // The run's host ends with the app, and its sessions with it (recorded for the next run's restore
  // question); quitting to install an update hands them over to the updated app's host instead.
  let ended = false;
  app.on("before-quit", (e) => {
    if (ended) return;
    e.preventDefault();
    ended = true;
    void host
      .quit(handingOver)
      .catch((err: unknown) => console.error("ending the host:", err))
      .finally(() => app.quit());
  });
}

