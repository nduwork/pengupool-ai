/** One row of Settings › Tailscale's checklist (settings.tailscale). */
export type TailscaleCheck = { id: string; label: string; ok: boolean; detail: string; fix?: { kind: "link" | "button"; label: string; url?: string; action?: string } };

/**
 * Why a pairing code can't be made yet, as the sentence shown, or null when it can. The host refuses a code until Serve is
 * set up (a saved Serve name), since the code would carry an address no device can reach; the
 * reason named is the checklist's first failing step.
 */
export function pairBlocker(serveName: string | undefined, checks: TailscaleCheck[]): string | null {
  if (serveName !== undefined && serveName !== "") return null;
  const first = checks.find((c) => !c.ok);
  return first === undefined ? "Set up Tailscale Serve first." : `Set up Tailscale Serve first. ${first.label}: ${first.detail.replace(/\.$/, "")}.`;
}
