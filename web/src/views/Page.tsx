import { Group, Panel, Separator, useDefaultLayout } from "react-resizable-panels";
import type { ReactElement, ReactNode } from "react";

import { cn } from "../lib/utils";

/** The header of a full-width page (Skill library, Teams, Settings). */
export function PageHeader({ title, subtitle, action }: { title: string; subtitle?: string; action?: ReactNode }): ReactElement {
  return (
    <div className="flex shrink-0 items-start gap-4 border-b border-border px-6 py-4">
      <div className="min-w-0 flex-1">
        <h1 className="text-title font-semibold">{title}</h1>
        {subtitle !== undefined ? <p className="text-label text-muted-foreground">{subtitle}</p> : null}
      </div>
      {action}
    </div>
  );
}

/**
 * A resize sash between two panels, as in VS Code: a hairline that highlights on hover and while
 * dragging, with a wider invisible grab area. Double-click resets the panel to its default size.
 */
export function Handle({ dir }: { dir: "x" | "y" }): ReactElement {
  return (
    <Separator
      className={cn(
        "relative z-10 shrink-0 bg-border outline-none transition-colors delay-75 hover:bg-primary data-[separator-state=drag]:bg-primary focus-visible:bg-primary",
        dir === "x" ? "w-px" : "h-px",
      )}
    >
      <span className={cn("absolute", dir === "x" ? "-inset-x-1 inset-y-0" : "-inset-y-1 inset-x-0")} />
    </Separator>
  );
}

/** A list on the left, the selected item's detail on the right; the list width is dragged and remembered per `id`. */
export function ListDetail({ id, list, detail, width = 280, padded = true }: { id: string; list: ReactNode; detail: ReactNode; width?: number; padded?: boolean }): ReactElement {
  const saved = useDefaultLayout({ id: `pengupool.split.${id}`, storage: localStorage });
  return (
    <Group orientation="horizontal" className="min-h-0 flex-1" defaultLayout={saved.defaultLayout} onLayoutChanged={saved.onLayoutChanged}>
      <Panel id="list" defaultSize={width} minSize={160} maxSize={520}>
        <div className={cn("h-full overflow-auto", padded && "p-2")}>{list}</div>
      </Panel>
      <Handle dir="x" />
      <Panel id="detail" minSize={320}>
        <div className="h-full min-w-0 overflow-hidden bg-panel">{detail}</div>
      </Panel>
    </Group>
  );
}
