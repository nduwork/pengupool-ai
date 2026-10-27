// The renderer's only door to native features. Sandboxed: no Node, just these calls.
import { contextBridge, ipcRenderer } from "electron";

contextBridge.exposeInMainWorld("penguAI", {
  platform: process.platform,
  arch: process.arch,
  connection: () => ipcRenderer.invoke("host:connection"),
  restartHost: () => ipcRenderer.invoke("host:restart"),
  openExternal: (url: string) => ipcRenderer.send("shell:openExternal", url),
  notify: (title: string, body: string) => ipcRenderer.send("app:notify", title, body),
  setBadge: (count: number) => ipcRenderer.send("app:badge", count),
});
