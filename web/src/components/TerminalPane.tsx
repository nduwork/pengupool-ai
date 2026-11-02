import { useEffect, useRef, type ReactElement, type ReactNode } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";

import { desktop, localDesktop } from "../app/desktop";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { toast } from "sonner";

import { AttachSession, type AttachEvents, type AttachState, type TerminalSink } from "../client/attach";
import type { Connection } from "../client/connection";
import { TERMINAL_QUERY_CSI, TERMINAL_QUERY_OSC } from "../protocol/queries";
import { TermCache, scrollbackSetting } from "./termCache";
import { PATH_MIME, UNSAFE_NOTICE, onTypePath, safePath, takePaths, typePath } from "../views/explorerPaths";

/** What Shift+Enter types in a session of this harness, a new line where a bare CR sends. pi gets Ctrl+J: its
 * input reads ESC CR as Alt+Enter, which submits (checked live on pi 0.84.3). Everything else gets ESC CR, as
 * Option+Enter types: Claude Code reads it as a new line; the other harnesses are not checked live.
 * TestShiftEnterPerHarness (internal/harness) lists every harness against this. */
export function shiftEnter(harness: string | undefined): string {
  return harness === "pi" ? "\n" : "\x1b\r";
}

/** Option on the Mac: Meta for keys (Option+Enter types ESC CR), and Option-drag selects text even
 * when the agent has turned mouse reporting on, as Claude Code does. Shift does not force a selection
 * on a Mac in xterm.js; Option-click does only with macOptionClickForcesSelection. */
export const macOptionKeys = { macOptionIsMeta: true, macOptionClickForcesSelection: true } as const;

export function isShiftEnter(e: Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "altKey" | "metaKey">): boolean {
  return e.key === "Enter" && e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey;
}

/** A terminal left behind by a session the user switched away from, and where its screen had got to. */
interface Kept {
  /** The process it was kept from (see `instanceOf`). */
  instance: string;
  term: Terminal;
  fit: FitAddon;
  retained: { generation: number; offset: bigint } | undefined;
  /** Replayed writes handed to xterm.js and not parsed yet; it parses on a later tick, so this travels
   * with the terminal rather than the mount. */
  replaying: { n: number };
}

/**
 * The last few sessions' terminals stay in memory when another session is selected, so their scrollback
 * survives the switch: coming back resumes from the worker's replay ring, from where the screen stopped.
 * Past this many the least recently used is dropped (and its history with it, until the next snapshot).
 */
const kept = new TermCache<Kept>(4, (k) => k.term.dispose());

/** Disposes the kept terminals of sessions no longer listed. */
export function pruneTerminals(live: ReadonlySet<string>): void {
  kept.prune(live);
}

export interface TerminalPaneProps {
  sessionId: string;
  /** The session's harness: it decides what Shift+Enter types. */
  harness?: string;
  /** The process behind the session (`instanceOf`); a terminal kept from another one is not resumed. */
  instance: string;
  channel: number;
  connection: Connection;
  /** The kind this page announced in `hello`; `ios` is view-only and never claims the lease. */
  clientKind: string;
  connected: boolean;
  sessionName: string;
  onAttach: (attach: AttachSession | null) => void;
  onState: (state: AttachState, detail: string) => void;
  onNotice: (notice: { code: string; message: string } | null) => void;
  /** Header content (title and session actions); the pane owns only the terminal. */
  header?: ReactNode;
}

export function TerminalPane({
  sessionId,
  harness,
  instance: instanceProp,
  channel,
  connection,
  clientKind,
  connected,
  sessionName,
  onAttach,
  onState,
  onNotice,
  header,
}: TerminalPaneProps): ReactElement {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const sessionRef = useRef<AttachSession | null>(null);
  const instanceRef = useRef(instanceProp);
  instanceRef.current = instanceProp;

  useEffect(() => {
    const host = hostRef.current;
    if (host === null) return;

    const instance = instanceRef.current;
    const back = kept.take(sessionId, instance);
    const term = back?.term ?? new Terminal({
      fontFamily: '"Geist Mono Variable", ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      cursorBlink: true,
      allowProposedApi: true,
      scrollback: scrollbackSetting(),
      ...macOptionKeys,
      theme: {
        background: "#0d1117",
        foreground: "#d6dde6",
        cursor: "#2ec4b6",
        cursorAccent: "#0d1117",
        selectionBackground: "#2ec4b655",
        black: "#1b1f27", red: "#f87171", green: "#4ade80", yellow: "#fbbf24",
        blue: "#60a5fa", magenta: "#c084fc", cyan: "#2ec4b6", white: "#d6dde6",
        brightBlack: "#6b7280", brightRed: "#fca5a5", brightGreen: "#86efac", brightYellow: "#fde68a",
        brightBlue: "#93c5fd", brightMagenta: "#d8b4fe", brightCyan: "#5eead4", brightWhite: "#f8fafc",
      },
    });
    const fit = back?.fit ?? new FitAddon();
    const replaying = back?.replaying ?? { n: 0 };
    if (back === undefined) {
      term.loadAddon(fit);
      const unicode = new Unicode11Addon();
      term.loadAddon(unicode);
      term.unicode.activeVersion = "11";
      // Links open in the user's browser, never inside the app window.
      term.loadAddon(new WebLinksAddon((_e, uri) => (desktop !== undefined ? desktop.openExternal(uri) : window.open(uri, "_blank"))));
      term.open(host);
      // GPU rendering like Orca; fall back to the DOM renderer if the context is lost or refused.
      try {
        const gl = new WebglAddon();
        gl.onContextLoss(() => gl.dispose());
        term.loadAddon(gl);
      } catch {
        /* DOM renderer */
      }
    } else {
      // A terminal kept from an earlier visit moves into this pane, scrollback and all.
      term.options.scrollback = scrollbackSetting();
      if (term.element !== undefined) host.appendChild(term.element);
    }
    fit.fit();
    term.focus();

    let claimIfFocused = (): void => {};
    let typeQueued = (): void => {};

    const events: AttachEvents = {
      state: (state, detail) => {
        onState(state, detail);
        // The app focuses the terminal at mount, before the attach completes, so a pane the user is
        // already looking at takes the size lease the moment the session becomes usable.
        if (state === "live" || state === "replay") claimIfFocused();
        if (state === "live") typeQueued();
      },
      error: (code, message) => onNotice({ code, message }),
      exit: (code, at) => {
        takePaths(sessionId); // nothing queued is typed into a later attach
        const when = at === undefined ? "" : ` at ${at}`;
        term.write(`\r\n\x1b[2m[process exited${code === undefined ? "" : ` with code ${code}`}${when}]\x1b[0m\r\n`);
      },
    };

    const session = new AttachSession({
      sessionId,
      channel,
      kind: clientKind,
      sink: terminalSink(term, replaying),
      send: (m) => connection.send(m),
      sendFrame: (type, seq, payload) =>
        connection.sendFrame({ type, channel, seq, length: payload.length }, payload),
      events,
    });
    if (back?.retained !== undefined) session.restore(back.retained);
    sessionRef.current = session;
    onAttach(session);

    // Swallow the queries xterm.js would answer, so no reply is ever generated while another
    // client holds the lease. Once we own the lease we are the responder and let xterm answer;
    // during replay we stay silent, because those bytes were already answered once.
    const silent = (): boolean => !(session.leaseOwner && session.attachState === "live");
    const queryHandlers: IDisposable[] = [
      ...TERMINAL_QUERY_CSI.map((id) => term.parser.registerCsiHandler(id, silent)),
      ...TERMINAL_QUERY_OSC.map((code) => term.parser.registerOscHandler(code, silent)),
      // OSC 52: what the agent copies (Claude Code's copy) lands on the clipboard of the device viewing
      // this pane. Live output only: a replayed copy must not overwrite the clipboard on reattach. Live
      // is judged when the bytes are handed to xterm.js, which parses them on a later tick.
      term.parser.registerOscHandler(52, osc52Handler(
        () => replaying.n === 0,
        (text) => void deliverCopy(text, navigator.clipboard, (m) => toast.error(m, { id: "osc52-copy" })),
      )),
    ];

    // Inbound wiring: data frames arrive on this session's channel, control messages (session
    // outcomes and the snapshot flow) go to the one attached session.
    const offChannel = connection.registerChannel(channel, (f) => session.handleFrame(f));
    const offControl = connection.onControl((m) => session.handleControl(m));

    const send = (data: string): void => {
      // The first keystroke is the other moment the browser may take the lease; the size comes from
      // the pane, never from the PTY's last known size.
      session.claimLease(term.cols, term.rows);
      session.input(data);
    };
    // Paths dropped on this session (here, or on its map tile) are typed into the prompt once it is live.
    typeQueued = (): void => {
      if (session.attachState !== "live" || clientKind === "ios") return;
      for (const p of takePaths(sessionId, Date.now(), (n) => toast.error(`Not typed: the session took too long to connect (${n} ${n === 1 ? "path" : "paths"}).`))) {
        term.focus();
        term.paste(shellQuote(p) + " ");
      }
    };
    const offType = onTypePath(sessionId, typeQueued);
    typeQueued();
    const input = term.onData(send);
    term.attachCustomKeyEventHandler((e) => {
      if (!isShiftEnter(e)) return true;
      if (e.type === "keydown") send(shiftEnter(harness));
      return false;
    });
    const resized = term.onResize(({ cols, rows }) => session.resize(cols, rows));
    session.resize(term.cols, term.rows);

    // The browser takes the size lease on explicit focus, on a click in the pane, or on the first
    // keystroke: those are the moments the user says "this is the pane I am using". A background tab
    // can do none of them, so it never resizes the agent. The claim always carries the pane's own
    // size, not the size the session learned from the PTY on attach.
    claimIfFocused = (): void => {
      if (!document.hasFocus() || document.activeElement !== term.textarea) return;
      session.claimLease(term.cols, term.rows);
    };
    const takeLease = (): void => session.claimLease(term.cols, term.rows);
    term.textarea?.addEventListener("focus", takeLease);
    host.addEventListener("mousedown", takeLease);
    // `term.focus()` above ran before the attach finished, so this only takes effect for a pane
    // whose session is already usable.
    claimIfFocused();
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(host);
    // Files dropped from Finder type their paths, shell-escaped, as Terminal.app does. Only when the
    // app works on this machine: a local path means nothing to an SSH host.
    // A row dragged from the Explorer types its path, quoted; the host's paths mean something on any host.
    const dragOver = (e: DragEvent): void => {
      if (e.dataTransfer?.types.includes("Files") || e.dataTransfer?.types.includes(PATH_MIME)) e.preventDefault();
    };
    const drop = (e: DragEvent): void => {
      const dragged = e.dataTransfer?.getData(PATH_MIME) ?? "";
      if (dragged !== "") {
        e.preventDefault();
        if (!typePath(sessionId, dragged)) onNotice({ code: "refused", message: UNSAFE_NOTICE });
        return;
      }
      const files = [...(e.dataTransfer?.files ?? [])];
      if (files.length === 0) return;
      e.preventDefault();
      const all = files.map((f) => localDesktop?.pathForFile(f) ?? "").filter((x) => x !== "");
      const paths = all.filter(safePath);
      if (paths.length < all.length) onNotice({ code: "refused", message: UNSAFE_NOTICE });
      if (paths.length === 0 || clientKind === "ios") return;
      term.focus();
      term.paste(paths.map(shellQuote).join(" ") + " ");
    };
    host.addEventListener("dragover", dragOver);
    host.addEventListener("drop", drop);

    return () => {
      observer.disconnect();
      term.textarea?.removeEventListener("focus", takeLease);
      host.removeEventListener("mousedown", takeLease);
      host.removeEventListener("dragover", dragOver);
      host.removeEventListener("drop", drop);
      for (const d of queryHandlers) d.dispose();
      offType();
      offControl();
      offChannel();
      input.dispose();
      resized.dispose();
      session.detach();
      sessionRef.current = null;
      onAttach(null);
      kept.put(sessionId, { instance, term, fit, retained: session.retainedState(), replaying });
    };
    // The pane is remounted per session (keyed by session id); connection identity is stable. Its
    // terminal outlives the mount (see `kept`).
  }, [sessionId, harness, channel, clientKind, connection, onAttach, onState, onNotice]);

  // Reconnect handling: re-attach with retained state when the daemon comes back.
  useEffect(() => {
    const session = sessionRef.current;
    if (session === null) return;
    if (connected) session.attach();
    else session.disconnected();
  }, [connected, sessionId]);

  return (
    <section className="flex h-full min-h-0 flex-col bg-terminal" aria-label={`Terminal: ${sessionName}`}>
      {header}
      <div className="min-h-0 flex-1" ref={hostRef} />
    </section>
  );
}

/** The text an OSC 52 clipboard write carries (`Pc;base64`), or undefined for a read query (`?`), an
 * empty payload or bad base64. Reads are never answered: a program in the session must not see the
 * viewer's clipboard. */
export function osc52Text(data: string): string | undefined {
  const sep = data.indexOf(";");
  const payload = sep < 0 ? "" : data.slice(sep + 1);
  if (payload === "" || payload === "?") return undefined;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(atob(payload), (c) => c.charCodeAt(0)));
  } catch {
    return undefined;
  }
}

/** Writes into xterm.js, counting replayed writes until xterm.js has parsed them (on a later tick). */
export function terminalSink(term: Pick<Terminal, "write" | "reset">, replaying: { n: number }): TerminalSink {
  return {
    write: (data, replayed) => {
      if (!replayed) return term.write(data);
      // Counted once the write is queued: a write xterm.js refuses (it throws past its pending limit)
      // never parses, so it must not hold OSC 52 off. The callback always runs on a later tick.
      term.write(data, () => replaying.n--);
      replaying.n++;
    },
    reset: () => term.reset(),
  };
}

export const COPY_BLOCKED = "Copy didn't reach the clipboard: this browser blocked it";

/** Puts an agent's copy on the clipboard. The app window and Chrome allow it; Safari and Firefox refuse
 * a write outside a click or key press, and plain HTTP has no clipboard, so `fail` says so. */
export async function deliverCopy(text: string, clipboard: Pick<Clipboard, "writeText"> | undefined, fail: (message: string) => void): Promise<void> {
  try {
    if (clipboard === undefined) throw new Error("no clipboard");
    await clipboard.writeText(text);
  } catch {
    fail(COPY_BLOCKED);
  }
}

/** The OSC 52 handler: a write goes to `write` while `live`; every OSC 52, read query included, is
 * consumed so nothing answers it. */
export function osc52Handler(live: () => boolean, write: (text: string) => void): (data: string) => boolean {
  return (data) => {
    const text = osc52Text(data);
    if (text !== undefined && live()) write(text);
    return true;
  };
}

/** A path as a shell reads it: anything beyond safe characters backslash-escaped (Terminal.app's form). */
export function shellQuote(path: string): string {
  return path.replace(/[^\w./~+,:@%=-]/g, "\\$&");
}
