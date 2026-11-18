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

/** One refused device-API request (muse.connection). */
export type MuseRefusal = { at: string; reason: string; host?: string; device?: string };

/** A refusal's reason in plain words. */
export function refusedReason(reason: string): string {
  switch (reason) {
    case "wrong_host":
      return "a device used the wrong address";
    case "revoked_token":
      return "a revoked device tried to connect";
    case "unknown_token":
      return "an unknown token";
    case "bad_code":
      return "a wrong or expired pairing code";
    case "pairing_paused":
      return "pairing was paused after too many wrong codes";
    default:
      return reason;
  }
}

/** Serve as muse.connection reports it. */
export type MuseServe = { state: "ok" | "problem"; url?: string; problem?: string; check?: string };

/** The Serve line above the device list. */
export function serveLine(serve: MuseServe): string {
  if (serve.state === "ok") return `Reachable at ${serve.url ?? "your tailnet"}`;
  return serve.problem ?? "Tailscale Serve isn't working.";
}
