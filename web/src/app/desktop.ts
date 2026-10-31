// The desktop app's preload bridge (desktop/src/preload.ts). Absent in a plain browser, where every
// caller falls back to web behaviour.
export interface DesktopBridge {
  platform: string;
  /** The Mac's processor as Node names it: arm64 (Apple Silicon) or x64 (Intel). */
  arch: string;
  /** The SSH host the app works in, or null for this machine. */
  environment: { id: string; name: string } | null;
  /** The running host's origin and token; the main process starts the host when needed. */
  connection(): Promise<{ url: string; token: string } | null>;
  pickFolder(): Promise<string | null>;
  /** A dropped file's path on this machine ("" when it has none). */
  pathForFile(file: File): string;
  openExternal(url: string): void;
  revealPath(path: string): void;
  /** A native notification; key names what it is about, so unnotify can withdraw it once answered. */
  notify(title: string, body: string, key?: string): void;
  /** Withdraws the notifications of these keys (their items were answered); older apps lack it. */
  unnotify?(keys: string[]): void;
  setBadge(count: number): void;
  /** Replaces this app with a disk image the host downloaded under <store>/updates, then restarts into it. */
  installUpdate(path: string): Promise<void>;
  restartHost(): Promise<void>;
  /** This machine and the SSH hosts set up in Settings; current is the SSH host in use, or null. */
  environments(): Promise<{ current: string | null; items: Environment[] }>;
  /** Works in another environment (null: this machine); the window reloads connected to it. */
  switchEnvironment(id: string | null, name?: string): Promise<void>;
}

export interface Environment {
  id: string;
  name: string;
  kind: string;
  address?: string;
  status: string;
  reason?: string;
}

declare global {
  interface Window {
    pengupool?: DesktopBridge;
  }
}

export const desktop: DesktopBridge | undefined = typeof window === "undefined" ? undefined : window.pengupool;

/** The desktop bridge when the app works on this machine: its folder picker and Finder reveal name
 * paths here, which mean nothing to an SSH host. */
export const localDesktop: DesktopBridge | undefined = desktop?.environment === null ? desktop : undefined;

/** The app is working on an SSH host: anything that opens a window on the host's own screen is off. */
export const onSSHHost = desktop?.environment != null;
