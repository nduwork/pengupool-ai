import { Compass, ExternalLink, MessagesSquare, Moon, PanelBottom, PanelLeft, Square, Sun } from "lucide-react";
import { Tooltip } from "radix-ui";
import { Group, Panel, useDefaultLayout, usePanelRef } from "react-resizable-panels";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement, type ReactNode } from "react";
import { Toaster, toast } from "sonner";

import { Ctx, harnessLabel, isAgent, useApp, usePersisted, type AppCtx, type EditorTab, type Nav } from "./app/ctx";
import { desktop, onSSHHost } from "./app/desktop";
import { AttachSession, type AttachState } from "./client/attach";
import { Connection, type ConnectionInfo } from "./client/connection";
import { TerminalPane, pruneTerminals } from "./components/TerminalPane";
import { instanceOf } from "./components/termCache";
import { Button } from "./components/ui/button";
import { Tip } from "./components/ui/controls";
import { cn } from "./lib/utils";
import { T } from "./protocol/control";
import { EMPTY_DOMAIN } from "./protocol/domain";
import { SessionsStore, type SessionsView } from "./state/sessions";
import { ApprovalsBell, NeedsYouBar } from "./views/Approvals";
import { Welcome } from "./views/GettingStarted";
import { NewSessionDialog } from "./views/NewSession";
import { UpdatePill } from "./views/Update";
import { Settings } from "./views/Settings";
import { Sidebar } from "./views/Sidebar";
import { SkillLibrary } from "./views/SkillLibrary";
import { isUnread, markViewed, type Viewed } from "./views/lanes";
import { sessionNeeds, type OpenGate } from "./views/inbox";
import { Teams } from "./views/Teams";
import { Handle } from "./views/Page";
import { Workbench, usePanelRefs } from "./views/Workspace";

/** The host's token: from the desktop bridge, or the `#token=` the host printed for a browser. */
function readToken(): string {
  try {
    const hash = window.location.hash.startsWith("#") ? window.location.hash.slice(1) : "";
    const token = new URLSearchParams(hash).get("token") ?? new URLSearchParams(window.location.search).get("token");
    if (token !== null && token !== "") {
      sessionStorage.setItem("pengupool.token", token);
      // Off the address bar and history: a previewed page's frame inherits this URL as its base.
      history.replaceState(null, "", window.location.pathname);
      return token;
    }
    return sessionStorage.getItem("pengupool.token") ?? "";
  } catch {
    return "";
  }
}

// Channels are per session and per connection; the worker echoes the one we pick.
const channelBySession = new Map<string, number>();
let nextChannel = 1;
function channelFor(sessionId: string): number {
  const existing = channelBySession.get(sessionId);
  if (existing !== undefined) return existing;
  const channel = nextChannel++;
  channelBySession.set(sessionId, channel);
  return channel;
}

const NAV_TITLE: Record<Nav, string> = { workspace: "Workspace", skills: "Helper agents", teams: "Teams", settings: "Settings" };

export function App(): ReactElement {
  // In the desktop app the page is loaded from the app bundle, so the host's address and token
  // come from the main process; in a browser the host served the page and printed the token.
  const [connection] = useState(() => new Connection({ token: desktop === undefined ? readToken() : "", baseURL: desktop === undefined ? "" : "http://127.0.0.1:1" }));
  const storeRef = useRef(new SessionsStore());
  const attachRef = useRef<AttachSession | null>(null);

  const [info, setInfo] = useState<ConnectionInfo>(() => connection.status);
  const [view, setView] = useState<SessionsView>(() => ({ rev: 0, epoch: "", sessions: [], domain: EMPTY_DOMAIN }));
  const [selected, setSelected] = useState<string | null>(null);
  const [, setAttachState] = useState<AttachState | null>(null);
  const [nav, setNav] = useState<Nav>("workspace");
  const [dialog, setDialog] = useState<ReactNode>(null);
  const [tabs, setTabs] = useState<EditorTab[]>([]);
  const [activeTab, setActiveTab] = useState("map");
  const [theme, setTheme] = usePersisted<"dark" | "light">("theme", window.matchMedia?.("(prefers-color-scheme: light)").matches === true ? "light" : "dark");
  const sidebarRef = usePanelRef();
  const shell = useDefaultLayout({ id: "pengupool.layout.shell", storage: localStorage });
  const panels = usePanelRefs();
  const [compassOpen, setCompassOpen] = usePersisted("compass.open", false);
  const [messagesOpen, setMessagesOpen] = usePersisted("messages.open", true);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  // Connect: ask the desktop main process for the host (it starts one when needed).
  useEffect(() => {
    let alive = true;
    if (desktop === undefined) {
      void connection.start();
      return;
    }
    void desktop.connection().then((c) => {
      if (alive && c !== null) connection.retarget(c.url, c.token);
    });
    return () => {
      alive = false;
    };
  }, [connection]);

  // A restarted host mints a new token: fetch it and reconnect.
  useEffect(() => {
    if (info.status !== "unauthorized" || desktop === undefined) return;
    void desktop.connection().then((c) => c !== null && connection.retarget(c.url, c.token));
  }, [info.status, connection]);

  useEffect(() => {
    const store = storeRef.current;
    const offStatus = connection.onStatus(setInfo);
    const offControl = connection.onControl((m) => {
      if (m.type === T.StateSnapshot || m.type === T.StateDelta) store.apply(m);
    });
    const offSessions = store.subscribe(setView);
    const offEpoch = store.onEpochChange(() => attachRef.current?.epochChanged());
    return () => {
      offStatus();
      offControl();
      offSessions();
      offEpoch();
    };
  }, [connection]);

  useEffect(() => {
    if (info.status !== "connected") return;
    const store = storeRef.current;
    connection.send({
      type: T.StateSubscribe,
      since_rev: store.currentRev === 0 ? undefined : store.currentRev,
      epoch: store.currentEpoch === "" ? undefined : store.currentEpoch,
    });
  }, [connection, info.status, info.connectionID]);

  // A closed or removed session's kept terminal goes with it.
  useEffect(() => {
    pruneTerminals(new Set(view.sessions.map((s) => s.id)));
  }, [view.sessions]);

  useEffect(() => {
    if (selected !== null && view.sessions.some((s) => s.id === selected)) return;
    const visible = view.sessions;
    const first = visible.find((s) => s.state === "running") ?? visible[0];
    setSelected(first?.id ?? null);
  }, [view, selected]);

  useEffect(() => {
    const timer = window.setInterval(() => attachRef.current?.flushAck(), 200);
    return () => window.clearInterval(timer);
  }, []);

  // The gates waiting on the owner, read again whenever a session changes status (a check-in arrives as one).
  const [gates, setGates] = useState<OpenGate[]>([]);
  const statusKey = view.sessions.map((s) => s.id + s.status).join();
  useEffect(() => {
    if (info.status === "connected") void connection.request<OpenGate[]>("gates.open", {}).then((r) => setGates(r ?? []), () => setGates([]));
  }, [connection, info.status, statusKey]);

  // Approvals: dock badge (pending proposals plus waiting agents), and a native notification for each new team launch proposal.
  const seen = useRef(new Set<string>());
  useEffect(() => {
    const pending = view.domain.approvals.filter((a) => a.state === "pending" && a.kind !== "permission");
    desktop?.setBadge(pending.length + sessionNeeds(view.sessions, gates).length);
    for (const a of pending) {
      if (seen.current.has(a.id)) continue;
      seen.current.add(a.id);
      desktop?.notify("Team launch ready", a.detail ?? "Review the proposal");
    }
  }, [view.domain.approvals, view.sessions, gates]);

  // A session starting to need you (a permission prompt, a lost connection) notifies on the transition only; finished work never does.
  const asking = useRef(new Set<string>());
  useEffect(() => {
    const list = sessionNeeds(view.sessions, gates);
    for (const n of list) if (!asking.current.has(n.session)) desktop?.notify(`${n.name} needs you`, n.kind === "checkin" ? `${n.name} asks: ${n.text}` : `${n.name} ${n.text}`);
    asking.current = new Set(list.map((n) => n.session));
  }, [view.sessions, gates]);

  // Unread dots: output since the owner last viewed a session (the selected one is always read).
  const [viewed, setViewed] = usePersisted<Viewed>("viewed", {});
  useEffect(() => setViewed(markViewed(viewed, view.sessions, selected, new Date().toISOString())), [view.sessions, selected]);
  const unread = useMemo(() => new Set(view.sessions.filter((s) => isUnread(s, viewed)).map((s) => s.id)), [view.sessions, viewed]);

  const call = useCallback(
    async <R,>(type: string, params?: unknown): Promise<R | undefined> => {
      try {
        return await connection.request<R>(type, params ?? {});
      } catch (e) {
        // Offline is already on the header's connection pill; a read made before the socket opens is no error.
        if ((e as { code?: string }).code !== "unavailable") toast.error((e as Error).message);
        return undefined;
      }
    },
    [connection],
  );

  const select = useCallback((id: string | null) => {
    setSelected(id);
    setNav("workspace");
    if (panels.terminal.current?.isCollapsed()) panels.terminal.current.expand();
    window.setTimeout(() => document.querySelector<HTMLTextAreaElement>(".xterm-helper-textarea")?.focus(), 60);
  }, [panels]);

  const openTab = useCallback((t: EditorTab) => {
    setTabs((xs) => (xs.some((x) => x.key === t.key) ? xs : [...xs, t]));
    setActiveTab(t.key);
    setNav("workspace");
  }, []);

  const toggle = (ref: { current: { isCollapsed(): boolean; collapse(): void; expand(): void } | null }): void => {
    const p = ref.current;
    if (p === null) return;
    if (p.isCollapsed()) p.expand();
    else p.collapse();
  };

  // Keyboard: ⌘N new session, ⌘1–4 pages, ⌘B sidebar, ⌘J terminal, ⌘\ Agent messages.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
      const k = e.key.toLowerCase();
      const pages: Nav[] = ["workspace", "skills", "teams", "settings"];
      if (k === "n" && !e.shiftKey) {
        e.preventDefault();
        setDialog(<NewSessionDialog onClose={() => setDialog(null)} />);
      } else if (["1", "2", "3", "4"].includes(k)) {
        e.preventDefault();
        setNav(pages[Number(k) - 1]);
      } else if (k === "b") {
        e.preventDefault();
        toggle(sidebarRef);
      } else if (k === "j") {
        e.preventDefault();
        toggle(panels.terminal);
      } else if (k === "\\") {
        e.preventDefault();
        setMessagesOpen(!messagesOpen);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [sidebarRef, panels, messagesOpen, setMessagesOpen]);

  const ctx: AppCtx = useMemo(
    () => ({
      conn: connection,
      connected: info.status === "connected",
      sessions: view.sessions,
      allSessions: view.sessions,
      domain: view.domain,
      selected,
      unread,
      gates,
      select,
      nav,
      setNav,
      call,
      openTab,
      showDialog: setDialog,
    }),
    [connection, info.status, view, selected, unread, gates, select, nav, call, openTab],
  );

  const onAttach = useCallback((attach: AttachSession | null) => {
    attachRef.current = attach;
    setAttachState(attach === null ? null : "detached");
  }, []);
  const onStateChange = useCallback((state: AttachState) => setAttachState(state), []);
  const onNotice = useCallback((next: { code: string; message: string } | null) => {
    if (next !== null) toast.error(next.message, { description: next.code });
  }, []);

  const selectedView = view.sessions.find((s) => s.id === selected);

  const terminal =
    selectedView === undefined ? (
      <div className="flex h-full flex-col items-center justify-center gap-3 bg-terminal text-label text-[#9aa1ae]">
        <p>No session selected.</p>
        <Button variant="default" onClick={() => setDialog(<NewSessionDialog onClose={() => setDialog(null)} />)}>
          New session
        </Button>
      </div>
    ) : selectedView.state !== "running" ? (
      <div className="flex h-full flex-col items-center justify-center gap-3 bg-terminal text-label text-[#c9d1d9]">
        <p>
          <strong>{selectedView.name}</strong> has {selectedView.state === "exited" ? "exited" : "stopped"}
          {selectedView.exit_code !== undefined ? ` (exit ${selectedView.exit_code})` : ""}.
        </p>
        <div className="flex gap-2">
          {isAgent(selectedView) ? (
            <Button variant="default" onClick={() => void call("session.resume", { session_id: selectedView.id })}>
              Resume conversation
            </Button>
          ) : null}
          <Button onClick={() => void call("session.forget", { session_id: selectedView.id })}>Remove from list</Button>
        </div>
      </div>
    ) : (
      <TerminalPane
        key={selectedView.id}
        sessionId={selectedView.id}
        instance={instanceOf(storeRef.current.currentEpoch, selectedView)}
        channel={channelFor(selectedView.id)}
        connection={connection}
        clientKind={connection.clientKind}
        connected={info.status === "connected"}
        sessionName={selectedView.name}
        onAttach={onAttach}
        onState={onStateChange}
        onNotice={onNotice}
        header={<TerminalHeader />}
      />
    );

  return (
    <Ctx.Provider value={ctx}>
      <Tooltip.Provider>
        <Group orientation="horizontal" className="h-screen" defaultLayout={shell.defaultLayout} onLayoutChanged={shell.onLayoutChanged}>
          <Panel id="sidebar" panelRef={sidebarRef} collapsible defaultSize={260} minSize={200} maxSize={420}>
            <Sidebar />
          </Panel>
          <Handle dir="x" />
          <Panel id="main" minSize={480}>
            <div className="flex h-full min-w-0 flex-col">
              <header className="drag flex h-11 shrink-0 items-center gap-2 border-b border-border px-3">
                <Tip label="Toggle sidebar (⌘B)">
                  <Button size="icon" variant="ghost" onClick={() => toggle(sidebarRef)} aria-label="Toggle sidebar">
                    <PanelLeft />
                  </Button>
                </Tip>
                <span className="text-label font-semibold">{NAV_TITLE[nav]}</span>
                {nav === "workspace" && selectedView !== undefined ? (
                  <span className="truncate text-label text-muted-foreground">
                    › {selectedView.name} <span className="text-faint">· {harnessLabel(selectedView.harness)}</span>
                  </span>
                ) : null}
                <span className="flex-1" />
                <ConnectionPill info={info} />
                {nav === "workspace" ? (
                  <>
                    <Tip label="Agent messages (⌘\)">
                      <Button size="icon" variant={messagesOpen ? "secondary" : "ghost"} onClick={() => setMessagesOpen(!messagesOpen)} aria-label="Toggle Agent messages">
                        <MessagesSquare />
                      </Button>
                    </Tip>
                    <Tip label={view.sessions.length === 0 ? "Start a session first" : "Compass"}>
                      <Button size="icon" variant={compassOpen ? "secondary" : "ghost"} disabled={view.sessions.length === 0} onClick={() => setCompassOpen(!compassOpen)} aria-label="Toggle Compass">
                        <Compass />
                      </Button>
                    </Tip>
                    <Tip label="Terminal (⌘J)">
                      <Button size="icon" variant="ghost" onClick={() => toggle(panels.terminal)} aria-label="Toggle terminal">
                        <PanelBottom />
                      </Button>
                    </Tip>
                  </>
                ) : null}
                <UpdatePill />
                <ApprovalsBell />
                <Tip label={theme === "dark" ? "Light theme" : "Dark theme"}>
                  <Button size="icon" variant="ghost" onClick={() => setTheme(theme === "dark" ? "light" : "dark")} aria-label="Toggle theme">
                    {theme === "dark" ? <Sun /> : <Moon />}
                  </Button>
                </Tip>
              </header>
              <NeedsYouBar />
              <main className="relative min-h-0 flex-1">
                {/* The workbench stays mounted on other pages, so terminals keep their attachment. */}
                <div className={cn("absolute inset-0", nav !== "workspace" && "invisible")}>
                  {view.sessions.length === 0 ? (
                    <Welcome />
                  ) : (
                    <Workbench
                      tabs={tabs}
                      activeTab={activeTab}
                      setActiveTab={setActiveTab}
                      closeTab={(k) => {
                        setTabs((xs) => xs.filter((x) => x.key !== k));
                        setActiveTab((cur) => (cur === k ? "map" : cur));
                      }}
                      terminal={terminal}
                      panels={panels}
                      compassOpen={compassOpen}
                      messagesOpen={messagesOpen}
                    />
                  )}
                </div>
                {nav !== "workspace" ? (
                  <div className="absolute inset-0 bg-background">{nav === "skills" ? <SkillLibrary /> : nav === "teams" ? <Teams /> : <Settings />}</div>
                ) : null}
              </main>
            </div>
          </Panel>
        </Group>
        {dialog}
        <Toaster theme={theme} position="bottom-right" richColors closeButton />
      </Tooltip.Provider>
    </Ctx.Provider>
  );
}

function ConnectionPill({ info }: { info: ConnectionInfo }): ReactElement {
  const tone = info.status === "connected" ? "bg-success" : info.status === "connecting" || info.status === "reconnecting" ? "bg-warning animate-pulse" : "bg-danger";
  const label = { connected: "Host connected", connecting: "Connecting…", reconnecting: "Reconnecting…", unauthorized: "Not authorized", incompatible: "Incompatible host", closed: "Disconnected" }[
    info.status
  ];
  return (
    <Tip label={info.detail}>
      <span className="no-drag flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5 text-caption text-muted-foreground">
        <span className={cn("size-1.5 rounded-full", tone)} />
        {label}
      </span>
    </Tip>
  );
}

function TerminalHeader(): ReactElement {
  const app = useApp();
  const s = app.sessions.find((x) => x.id === app.selected);
  const [confirm, setConfirm] = useState(false);
  if (s === undefined) return <></>;
  return (
    <div className="flex h-9 shrink-0 items-center gap-2 border-b border-white/10 px-3 text-[#c9d1d9]">
      <span className="text-label font-semibold">{s.name}</span>
      <span className="font-mono text-micro text-[#8b949e]">
        {harnessLabel(s.harness)} · {s.cols}×{s.rows}
        {s.context_pct !== undefined ? ` · ${s.context_pct}% context` : ""}
      </span>
      {!isAgent(s) ? (
        <span className="rounded-sm bg-[#f59e0b]/20 px-1.5 text-micro text-[#fcd34d]" title="Started before sessions were bound to an agent. Stop it and start a Claude Code or OMP session instead.">
          Not an agent session
        </span>
      ) : null}
      <span className="flex-1" />
      {onSSHHost ? null : (
        <Tip label="Open in your terminal app">
          <button type="button" className="flex h-6 items-center gap-1 rounded-md px-2 text-caption hover:bg-white/10" onClick={() => void app.call("session.open_terminal", { session_id: s.id })}>
            <ExternalLink className="size-3.5" /> Open in Terminal
          </button>
        </Tip>
      )}
      {confirm ? (
        <>
          <button
            type="button"
            className="h-6 rounded-md bg-[#f87171]/20 px-2 text-caption text-[#fca5a5] hover:bg-[#f87171]/30"
            onClick={() => {
              setConfirm(false);
              app.conn.send({ type: T.SessionKill, session_id: s.id });
            }}
          >
            Stop {s.name}
          </button>
          <button type="button" className="h-6 rounded-md px-2 text-caption hover:bg-white/10" onClick={() => setConfirm(false)}>
            Keep
          </button>
        </>
      ) : (
        <button type="button" className="flex h-6 items-center gap-1 rounded-md px-2 text-caption hover:bg-white/10" onClick={() => setConfirm(true)}>
          <Square className="size-3" /> Stop
        </button>
      )}
    </div>
  );
}
