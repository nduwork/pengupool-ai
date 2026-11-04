/** The host's triage.status: the model PenguPool needs, set up and answering, or not. */
export interface TriageStatus {
  status: "missing" | "unchecked" | "unreachable" | "ok";
  error?: string;
  /** With missing: the active model PenguPool removed because triage no longer speaks its format. */
  removed?: RemovedModel;
}

/** A saved triage model the host removed (decision.removed). */
export interface RemovedModel {
  type: string;
  format: string;
  model?: string;
  active?: boolean;
}

/** "Ollama chat format": the format a removed model spoke, as the app names it. */
export function formatName(r: RemovedModel): string {
  if (r.format !== "openai-chat") return `${r.format} format`;
  return r.type === "ollama" ? "Ollama chat format" : "chat format";
}

/** "local triage model (llama3.1:8b, Ollama chat format)": how the app names a removed model. */
export function removedName(r: RemovedModel): string {
  return `${r.type === "ollama" ? "local" : "online"} triage model (${r.model ? `${r.model}, ` : ""}${formatName(r)})`;
}

/** What the app says while the triage model is missing or not answering; null when it works. */
export function triagePrompt(t: TriageStatus | null): string | null {
  if (t === null || t.status === "ok" || t.status === "unchecked") return null; // unchecked: its check is running
  if (t.status === "missing" && t.removed !== undefined) {
    const n = removedName(t.removed);
    return `The ${n} was removed: PenguPool's triage now uses System One decision models only. Set one up in Settings.`;
  }
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

/** The Spark tab's note for a Meta -contributor model id, whose prompts and answers Meta may train on; null otherwise. */
export function contributorNote(model: string | undefined): string | null {
  return model?.trim().endsWith("-contributor") === true ? "Contributor mode: Meta may train on the tasks sent for triage." : null;
}
