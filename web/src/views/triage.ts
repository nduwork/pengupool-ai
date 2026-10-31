/** The host's triage.status: the model PenguPool needs, set up and answering, or not. */
export interface TriageStatus {
  status: "missing" | "unchecked" | "unreachable" | "ok";
  error?: string;
}

/** What the app says while the triage model is missing or not answering; null when it works. */
export function triagePrompt(t: TriageStatus | null): string | null {
  if (t === null || t.status === "ok" || t.status === "unchecked") return null; // unchecked: its check is running
  return t.status === "missing"
    ? "Set up a triage model: PenguPool's memory, check-ins and team proposals need one. Pick a local (Ollama) or remote model in Settings."
    : `The triage model is not answering${t.error ? ` (${t.error})` : ""}: PenguPool's memory and check-ins wait until it does. Check it in Settings.`;
}

/**
 * Reads the triage status now and every intervalMs, handing each answer to set: a model whose check
 * fails after the first read (it starts unchecked), or that dies mid-day, shows its prompt without
 * any other change. Returns the stop function.
 */
export function watchTriage(read: () => Promise<TriageStatus | null>, set: (t: TriageStatus | null) => void, intervalMs = 30_000): () => void {
  const load = (): void => void read().then(set, () => set(null));
  load();
  const t = setInterval(load, intervalMs);
  return () => clearInterval(t);
}
