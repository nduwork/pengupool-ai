import { Check, ChevronDown, Laptop, Loader2, Server, Settings as SettingsIcon } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { useState, type ReactElement } from "react";

import { useApp } from "../app/ctx";
import { desktop, type Environment } from "../app/desktop";
// The mark below 32 px is the small cut; on the dark theme it sits on its ice tile, as the colour mark
// must never go straight onto a dark background (the navy bodies disappear).
import logo from "../../../assets/logo-16.svg";
import logoTile from "../../../assets/logo-16-tile.svg";
import { cn } from "../lib/utils";

// The environment switcher at the top of the window: the app works on this machine or on one SSH
// host at a time, like a remote window in an editor. Picking one reloads the window connected to
// that host's PenguPool, which the main process sets up (login, install or update, port forward).

const itemCls = "flex h-8 cursor-default select-none items-center gap-2 rounded-md px-2 text-label outline-none data-[disabled]:opacity-40 data-[highlighted]:bg-hover";

export function EnvSwitcher(): ReactElement {
  const app = useApp();
  const [items, setItems] = useState<Environment[] | null>(null);
  const [switching, setSwitching] = useState<string | null>(null);
  if (desktop === undefined) return (
      <span className="flex items-center gap-1.5 text-body font-semibold tracking-tight">
        <img src={logo} alt="" className="size-5 dark:hidden" />
        <img src={logoTile} alt="" className="hidden size-5 dark:block" />
        PenguPool
      </span>
    );
  const current = desktop.environment;
  const hosts = (items ?? []).filter((e) => e.kind === "ssh");
  const pick = (id: string | null, name: string): void => {
    if (id === (current?.id ?? null)) return;
    setSwitching(name);
    void desktop?.switchEnvironment(id, name);
  };
  return (
    <DropdownMenu.Root onOpenChange={(open) => open && void desktop?.environments().then((r) => setItems(r.items))}>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          className={cn(
            "flex h-7 min-w-0 items-center gap-1.5 rounded-md px-2 text-label font-semibold hover:bg-hover",
            current !== null && "bg-primary/10 text-primary hover:bg-primary/15",
          )}
          aria-label="Switch environment"
        >
          {switching !== null ? <Loader2 className="size-3.5 shrink-0 animate-spin" /> : current !== null ? <Server className="size-3.5 shrink-0" /> : <Laptop className="size-3.5 shrink-0" />}
          <span className="truncate">{switching ?? current?.name ?? "This machine"}</span>
          <ChevronDown className="size-3.5 shrink-0 opacity-60" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" sideOffset={4} className="z-50 max-h-96 min-w-64 overflow-auto rounded-lg border border-border bg-raised p-1 shadow-2xl">
          <DropdownMenu.Label className="px-2 py-1 text-caption text-faint">Work on</DropdownMenu.Label>
          <DropdownMenu.Item className={itemCls} onSelect={() => pick(null, "This machine")}>
            <Laptop className="size-3.5 text-muted-foreground" />
            This machine
            {current === null ? <Check className="ml-auto size-3.5 text-primary" /> : null}
          </DropdownMenu.Item>
          {items === null ? <p className="px-2 py-1.5 text-caption text-faint">Loading hosts…</p> : null}
          {hosts.map((e) => (
            <DropdownMenu.Item key={e.id} className={itemCls} disabled={e.status === "locked"} onSelect={() => pick(e.id, e.name)}>
              <Server className="size-3.5 text-muted-foreground" />
              <span className="min-w-0">
                <span className="block truncate">{e.name}</span>
                <span className="block truncate font-mono text-micro text-faint">{e.status === "locked" ? "needs password — set it in Settings" : e.address}</span>
              </span>
              {current?.id === e.id ? <Check className="ml-auto size-3.5 shrink-0 text-primary" /> : null}
            </DropdownMenu.Item>
          ))}
          {items !== null && hosts.length === 0 ? <p className="px-2 py-1.5 text-caption text-muted-foreground">No SSH hosts yet.</p> : null}
          <DropdownMenu.Separator className="my-1 h-px bg-border" />
          <DropdownMenu.Item
            className={itemCls}
            disabled={current !== null}
            onSelect={() => {
              try {
                localStorage.setItem("pengupool.settings.section", JSON.stringify("nodes"));
              } catch {
                /* the section just opens at its default */
              }
              app.setNav("settings");
            }}
          >
            <SettingsIcon className="size-3.5 text-muted-foreground" />
            {current !== null ? "Manage SSH hosts from This machine" : "Manage SSH hosts…"}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
