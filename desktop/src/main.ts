// PenguPool AI desktop: main process. It supervises the pengu owner API,
// serves the UI from the app bundle on the app:// scheme, and gives the
// renderer a small native bridge (notifications, dock badge, external
// links). The renderer talks to the owner API directly over HTTP on
// localhost with the same-machine token.
import { BrowserWindow, Menu, Notification, app, ipcMain, protocol, shell } from "electron";
import { join } from "node:path";
import { readFile } from "node:fs/promises";
import { Host, type OwnerConnection } from "./host";

app.setName("PenguPool AI");

const packaged = app.isPackaged;
const rendererRoot = packaged
  ? join(process.resourcesPath, "renderer")
  : join(__dirname, "..", "..", "internal", "webapp", "dist");
const host = new Host(
  packaged
    ? join(process.resourcesPath, "bin", "pengu")
    : join(__dirname, "..", "..", "bin", "pengu"),
);

protocol.registerSchemesAsPrivileged([
  { scheme: "app", privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: true } },
]);

let win: BrowserWindow | null = null;
let current: OwnerConnection | null = null;

async function connection(): Promise<OwnerConnection> {
  if (current === null) current = await host.ensure();
  return current;
}

function createWindow(): void {
  win = new BrowserWindow({
    width: 1100,
    height: 750,
    title: "PenguPool AI",
    webPreferences: {
      preload: join(__dirname, "preload.js"),
      sandbox: true,
      contextIsolation: true,
    },
  });
  win.on("closed", () => {
    win = null;
  });
  void win.loadURL("app://renderer/index.html");
}

function serveRenderer(): void {
  protocol.handle("app", async (req) => {
    const url = new URL(req.url);
    const path = decodeURIComponent(url.pathname.replace(/^\/renderer\//, ""));
    const file = join(rendererRoot, path === "" || path.endsWith("/") ? join(path, "index.html") : path);
    try {
      const data = await readFile(file);
      const type =
        file.endsWith(".html") ? "text/html"
        : file.endsWith(".js") ? "text/javascript"
        : file.endsWith(".css") ? "text/css"
        : file.endsWith(".svg") ? "image/svg+xml"
        : "application/octet-stream";
      return new Response(data, { headers: { "Content-Type": type } });
    } catch {
      return new Response("not found", { status: 404 });
    }
  });
}

function ipc(): void {
  ipcMain.handle("host:connection", () => connection());
  ipcMain.handle("host:restart", async () => {
    current = await host.restart();
    return current;
  });
  ipcMain.on("shell:openExternal", (_e, url: string) => {
    if (/^https?:\/\//.test(url)) void shell.openExternal(url);
  });
  ipcMain.on("app:notify", (_e, title: string, body: string) => {
    new Notification({ title, body }).show();
  });
  ipcMain.on("app:badge", (_e, count: number) => {
    app.setBadgeCount(count);
  });
}

function menu(): void {
  Menu.setApplicationMenu(
    Menu.buildFromTemplate([
      {
        label: "PenguPool AI",
        submenu: [
          { role: "about" },
          { type: "separator" },
          { role: "services" },
          { type: "separator" },
          { role: "hide" },
          { role: "hideOthers" },
          { role: "unhide" },
          { type: "separator" },
          { role: "quit" },
        ],
      },
      { role: "editMenu" },
      { role: "windowMenu" },
    ]),
  );
}

const single = app.requestSingleInstanceLock();
if (!single) {
  app.quit();
} else {
  void app.whenReady().then(() => {
    serveRenderer();
    ipc();
    menu();
    createWindow();
    app.on("activate", () => {
      if (win === null) createWindow();
    });
  });
  app.on("window-all-closed", () => {
    if (process.platform !== "darwin") app.quit();
  });
}
