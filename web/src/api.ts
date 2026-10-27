// Typed bridge injected by desktop/src/preload.ts. In plain-browser dev
// the bridge is absent and the app asks for URL + token once.
export interface Bridge {
  platform: string;
  arch: string;
  connection: () => Promise<{ url: string; token: string }>;
  restartHost: () => Promise<{ url: string; token: string }>;
  openExternal: (url: string) => void;
  notify: (title: string, body: string) => void;
  setBadge: (count: number) => void;
}

declare global {
  interface Window {
    penguAI?: Bridge;
  }
}

export async function connection(): Promise<{ url: string; token: string }> {
  if (window.penguAI) return window.penguAI.connection();
  const url = localStorage.getItem("pengu-url") ?? "http://127.0.0.1:8471";
  let token = localStorage.getItem("pengu-token") ?? "";
  if (token === "") {
    token = window.prompt("Owner API token (store owner-token file)", "") ?? "";
    localStorage.setItem("pengu-url", url);
    localStorage.setItem("pengu-token", token);
  }
  return { url, token };
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const { url, token } = await connection();
  const res = await fetch(url + path, {
    ...init,
    headers: { ...(init?.headers ?? {}), Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export interface Line {
  id: string;
  rule: string;
  confidence: number;
  source: string;
}

export interface Proposal {
  id: string;
  words: string;
  kind: string;
  status: string;
}

export interface SessionRow {
  id: string;
  agent: string;
  alive: boolean;
  task: string;
}

export interface InboxEntry {
  id: string;
  from: string;
  text: string;
}

export interface ResultRow {
  task: string;
  arm: string;
  matched: boolean | null;
  interruptions: number;
  passed: boolean | null;
}
