import { forwardRef, type InputHTMLAttributes, type KeyboardEvent, type LabelHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { Switch as RSwitch, Tooltip as RTooltip } from "radix-ui";

import { cn } from "../../lib/utils";

const field =
  "w-full rounded-md border border-input bg-panel px-2.5 text-body text-foreground placeholder:text-faint focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input({ className, ...p }, ref) {
  return <input ref={ref} className={cn(field, "h-8", className)} {...p} />;
});

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(function Textarea({ className, ...p }, ref) {
  return <textarea ref={ref} className={cn(field, "min-h-20 py-2 resize-y", className)} {...p} />;
});

/** For a textarea in a form: Enter saves it, Shift+Enter adds a line (not while an IME is composing). */
export function submitOnEnter(e: KeyboardEvent<HTMLTextAreaElement>): void {
  if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return;
  e.preventDefault();
  e.currentTarget.form?.requestSubmit();
}

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(function Select({ className, ...p }, ref) {
  return <select ref={ref} className={cn(field, "h-8 pr-7", className)} {...p} />;
});

/** A field with its caption. hintId names the caption, for the field's aria-describedby (an aria-label
 * on the field replaces the label, so the caption is not read otherwise). */
export function Label({ className, children, hint, hintId, ...p }: LabelHTMLAttributes<HTMLLabelElement> & { hint?: ReactNode; hintId?: string }): React.ReactElement {
  return (
    <label className={cn("grid gap-1.5 text-label font-medium text-foreground", className)} {...p}>
      {children}
      {hint !== undefined ? (
        <span id={hintId} className="text-caption font-normal text-muted-foreground">
          {hint}
        </span>
      ) : null}
    </label>
  );
}

export function Switch({ checked, onCheckedChange, label }: { checked: boolean; onCheckedChange: (v: boolean) => void; label?: string }): React.ReactElement {
  return (
    <RSwitch.Root
      checked={checked}
      onCheckedChange={onCheckedChange}
      aria-label={label}
      className="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent bg-input transition-colors data-[state=checked]:bg-primary"
    >
      <RSwitch.Thumb className="block size-4 translate-x-0.5 rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-4" />
    </RSwitch.Root>
  );
}

export function Badge({ className, tone = "neutral", children }: { className?: string; tone?: "neutral" | "primary" | "success" | "warning" | "danger" | "info"; children: ReactNode }): React.ReactElement {
  const tones = {
    neutral: "bg-muted text-muted-foreground border-border",
    primary: "bg-primary/10 text-primary border-primary/30",
    success: "bg-success/10 text-success border-success/30",
    warning: "bg-warning/10 text-warning border-warning/30",
    danger: "bg-danger/10 text-danger border-danger/30",
    info: "bg-info/10 text-info border-info/30",
  };
  return <span className={cn("inline-flex items-center gap-1 rounded-sm border px-1.5 py-px text-micro font-medium", tones[tone], className)}>{children}</span>;
}

/** A team member's role: derived from the team's tree (views/Formation.tsx teamLabel), never stored. */
export type Role = "lead" | "sme" | "helper";

// Cues, no hue, so the tag never competes with the status colours: the box, the weight and the word.
const ROLE: Record<Role, { word: string; cls: string; tip: string }> = {
  lead: { word: "Lead", cls: "border-muted-foreground text-foreground font-semibold", tip: "Lead: the session at the root of the team's tree" },
  sme: { word: "SME", cls: "border-border text-muted-foreground font-medium", tip: "SME: a member you added, working in its own repository" },
  helper: { word: "Helper", cls: "border-transparent text-muted-foreground", tip: "Helper: an agent from the helper pool" },
};

export function RoleTag({ role, className }: { role: Role; className?: string }): React.ReactElement {
  const r = ROLE[role];
  return (
    <span title={r.tip} className={cn("inline-flex shrink-0 items-center rounded-sm border px-1 text-micro leading-[14px]", r.cls, className)}>
      {r.word}
    </span>
  );
}

export function Kbd({ children }: { children: ReactNode }): React.ReactElement {
  return <kbd className="rounded border border-border bg-muted px-1 font-mono text-micro text-muted-foreground">{children}</kbd>;
}

/** A tip never runs off the screen: it keeps clear of the edges and, taller than the room it has,
 * scrolls. The pointer may move into it (Radix's hoverable content), so it stays open to scroll. */
export const TIP_CONTENT = "z-50 max-h-[var(--radix-tooltip-content-available-height)] overflow-y-auto rounded-md border border-border bg-raised px-2 py-1 text-caption text-foreground shadow-lg";

export function Tip({ label, children, side = "bottom" }: { label: ReactNode; children: React.ReactElement; side?: "top" | "bottom" | "left" | "right" }): React.ReactElement {
  return (
    <RTooltip.Root delayDuration={350}>
      <RTooltip.Trigger asChild>{children}</RTooltip.Trigger>
      <RTooltip.Portal>
        <RTooltip.Content side={side} sideOffset={6} collisionPadding={8} className={TIP_CONTENT}>
          {label}
        </RTooltip.Content>
      </RTooltip.Portal>
    </RTooltip.Root>
  );
}

export function Empty({ icon, title, children }: { icon?: ReactNode; title: string; children?: ReactNode }): React.ReactElement {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      {icon !== undefined ? <div className="text-faint [&_svg]:size-8">{icon}</div> : null}
      <p className="text-title font-medium">{title}</p>
      {children !== undefined ? <div className="max-w-md text-label text-muted-foreground">{children}</div> : null}
    </div>
  );
}

export function Card({ className, children }: { className?: string; children: ReactNode }): React.ReactElement {
  return <div className={cn("rounded-lg border border-border bg-panel", className)}>{children}</div>;
}

export function SectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }): React.ReactElement {
  return (
    <div className="flex items-center justify-between gap-2 px-2 pt-3 pb-1">
      <span className="text-micro font-semibold uppercase tracking-wider text-faint">{children}</span>
      {action}
    </div>
  );
}
