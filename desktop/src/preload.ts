// The renderer's only door to native features (web/src/app/desktop.ts describes it). Sandboxed:
// no Node, just these calls.
import { contextBridge, ipcRenderer, webUtils } from "electron";

contextBridge.exposeInMainWorld("pengupool", {
  platform: process.platform,
  arch: process.arch,
  environment: ipcRenderer.sendSync("env:current") as { id: string; name: string } | null,
  connection: () => ipcRenderer.invoke("host:connection"),
  restartHost: () => ipcRenderer.invoke("host:restart"),
  environments: () => ipcRenderer.invoke("env:list"),
  switchEnvironment: (id: string | null, name?: string) => ipcRenderer.invoke("env:switch", id, name),
  pickFolder: () => ipcRenderer.invoke("dialog:pickFolder"),
  pathForFile: (file: File) => webUtils.getPathForFile(file),
  openExternal: (url: string) => ipcRenderer.send("shell:openExternal", url),
  revealPath: (path: string) => ipcRenderer.send("shell:reveal", path),
  notify: (title: string, body: string) => ipcRenderer.send("app:notify", title, body),
  setBadge: (count: number) => ipcRenderer.send("app:badge", count),
  installUpdate: (path: string) => ipcRenderer.invoke("update:install", path),
});
