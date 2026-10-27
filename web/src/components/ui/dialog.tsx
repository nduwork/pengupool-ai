import { Dialog as RDialog } from "radix-ui";
import { X } from "lucide-react";
import type { ReactElement, ReactNode } from "react";

import { cn } from "../../lib/utils";

/** A modal sheet (Radix Dialog): focus trap, Esc to close, portal above everything. */
export function Sheet({
  title,
  description,
  onClose,
  children,
  wide,
  footer,
}: {
  title: string;
  description?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
  footer?: ReactNode;
}): ReactElement {
  return (
    <RDialog.Root open onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 z-40 bg-black/50 backdrop-blur-[1px]" />
        <RDialog.Content
          className={cn(
            "fixed left-1/2 top-[12vh] z-50 flex max-h-[76vh] w-[min(560px,94vw)] -translate-x-1/2 flex-col rounded-xl border border-border bg-panel shadow-2xl",
            wide === true && "w-[min(880px,96vw)]",
          )}
        >
          <div className="flex items-start justify-between gap-4 border-b border-border px-5 py-4">
            <div>
              <RDialog.Title className="text-title font-semibold">{title}</RDialog.Title>
              {description !== undefined ? (
                <RDialog.Description className="mt-0.5 text-label text-muted-foreground">{description}</RDialog.Description>
              ) : (
                <RDialog.Description className="sr-only">{title}</RDialog.Description>
              )}
            </div>
            <RDialog.Close className="rounded-md p-1 text-muted-foreground hover:bg-hover" aria-label="Close">
              <X className="size-4" />
            </RDialog.Close>
          </div>
          {children !== null ? <div className="min-h-0 flex-1 overflow-auto px-5 py-4">{children}</div> : null}
          {footer !== undefined ? <div className="flex justify-end gap-2 border-t border-border px-5 py-3">{footer}</div> : null}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}
