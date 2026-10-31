/** The host's triage.status: the model PenguPool needs, set up and answering, or not. */
export interface TriageStatus {
  status: "missing" | "unreachable" | "ok";
  error?: string;
}

/** What the app says while the triage model is missing or not answering; null when it works. */
export function triagePrompt(t: TriageStatus | null): string | null {
  if (t === null || t.status === "ok") return null;
  return t.status === "missing"
    ? "Set up a triage model: PenguPool's memory, check-ins and team proposals need one. Pick a local (Ollama) or remote model in Settings."
    : `The triage model is not answering${t.error ? ` (${t.error})` : ""}: PenguPool's memory and check-ins wait until it does. Check it in Settings.`;
}
