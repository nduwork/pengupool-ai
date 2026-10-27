import { FileDiff, FileText, Map as MapIcon, X } from "lucide-react";
import { Group, Panel, useDefaultLayout, usePanelRef, type PanelImperativeHandle } from "react-resizable-panels";
import { useEffect, type ReactElement, type ReactNode, type RefObject } from "react";

import { type EditorTab } from "../app/ctx";
import { cn } from "../lib/utils";
import { CompassPanel } from "./CompassPanel";
import { DiffView, FileView } from "./Editors";
import { MessageLog, TeamMap } from "./map/Map";
import { Handle } from "./Page";

// The terminal-first workbench: the map (or a file / diff tab) upper centre with the Compass beside it
// when open, the terminal below with Agent messages to its right when open. Panels resize and
// collapse; selecting a session never changes their sizes.

export interface PanelRefs {
  center: RefObject<PanelImperativeHandle | null>;
  terminal: RefObject<PanelImperativeHandle | null>;
}

export function usePanelRefs(): PanelRefs {
  return { center: usePanelRef(), terminal: usePanelRef() };
}

export function Workbench({
  tabs,
  activeTab,
  setActiveTab,
  closeTab,
  terminal,
  panels,
  compassOpen,
  messagesOpen,
}: {
  tabs: EditorTab[];
  activeTab: string;
  setActiveTab: (k: string) => void;
  closeTab: (k: string) => void;
  terminal: ReactNode;
  panels: PanelRefs;
  compassOpen: boolean;
  messagesOpen: boolean;
}): ReactElement {
  const tab = tabs.find((t) => t.key === activeTab);
  const outer = useDefaultLayout({ id: "pengupool.layout.v", storage: localStorage });
  const inner = useDefaultLayout({ id: "pengupool.layout.h", storage: localStorage });
  const lower = useDefaultLayout({ id: "pengupool.layout.lower", storage: localStorage });

  // ⌘W closes the active editor tab.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "w" && tab !== undefined) {
        e.preventDefault();
        closeTab(tab.key);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [tab, closeTab]);

  return (
    <Group orientation="vertical" className="h-full" defaultLayout={outer.defaultLayout} onLayoutChanged={outer.onLayoutChanged}>
      <Panel id="upper" minSize={120} defaultSize="55">
        <Group orientation="horizontal" className="h-full" defaultLayout={inner.defaultLayout} onLayoutChanged={inner.onLayoutChanged}>
          <Panel id="center" panelRef={panels.center} collapsible minSize={240} defaultSize="72">
            <section className="flex h-full min-h-0 flex-col bg-panel">
              <div className="flex h-9 shrink-0 items-end gap-px overflow-x-auto border-b border-border bg-background px-1" role="tablist">
                <TabButton active={tab === undefined} onClick={() => setActiveTab("map")} icon={<MapIcon />}>
                  Map
                </TabButton>
                {tabs.map((t) => (
                  <TabButton
                    key={t.key}
                    active={activeTab === t.key}
                    onClick={() => setActiveTab(t.key)}
                    icon={t.kind === "diff" ? <FileDiff /> : <FileText />}
                    title={t.path}
                    onClose={() => closeTab(t.key)}
                  >
                    {t.title}
                    {t.kind === "diff" ? <span className="text-faint"> {t.staged === true ? "(staged)" : "(diff)"}</span> : null}
                  </TabButton>
                ))}
              </div>
              <div className="min-h-0 flex-1 overflow-auto">{tab === undefined ? (
                <TeamMap />
              ) : tab.kind === "file" ? <FileView key={tab.key} tab={tab} /> : <DiffView key={tab.key} tab={tab} />}</div>
            </section>
          </Panel>
          {compassOpen ? (
            <>
              <Handle dir="x" />
              <Panel id="compass" minSize={280} defaultSize="30">
                <CompassPanel />
              </Panel>
            </>
          ) : null}
        </Group>
      </Panel>
      <Handle dir="y" />
      {/* The terminal stays mounted while collapsed, so its attachment and history survive. */}
      <Panel id="terminal" panelRef={panels.terminal} collapsible minSize={120} defaultSize="45">
        <Group orientation="horizontal" className="h-full" defaultLayout={lower.defaultLayout} onLayoutChanged={lower.onLayoutChanged}>
          <Panel id="term" minSize={240} defaultSize="70">
            {terminal}
          </Panel>
          {messagesOpen ? (
            <>
              <Handle dir="x" />
              <Panel id="messages" minSize={280} defaultSize="35">
                <MessageLog />
              </Panel>
            </>
          ) : null}
        </Group>
      </Panel>
    </Group>
  );
}

function TabButton({
  active,
  onClick,
  icon,
  children,
  title,
  onClose,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactElement;
  children: ReactNode;
  title?: string;
  onClose?: () => void;
}): ReactElement {
  return (
    <div
      className={cn(
        "group flex h-8 shrink-0 items-center gap-1.5 rounded-t-md border border-b-0 px-2.5 text-label",
        active ? "border-border bg-panel text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      <button type="button" role="tab" aria-selected={active} onClick={onClick} title={title} className="flex items-center gap-1.5 [&_svg]:size-3.5">
        {icon}
        <span className="max-w-48 truncate">{children}</span>
      </button>
      {onClose !== undefined ? (
        <button type="button" onClick={onClose} aria-label="Close tab" className="rounded p-0.5 text-faint opacity-0 hover:bg-hover group-hover:opacity-100 aria-[selected]:opacity-100">
          <X className="size-3" />
        </button>
      ) : null}
    </div>
  );
}
