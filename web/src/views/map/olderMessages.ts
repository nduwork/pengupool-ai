/** A message from a team's record (`team.messages`), the whole history; the live state keeps only the latest. */
export interface RecordedMessage {
  from: string;
  to: string;
  at: string;
  text: string;
}

/** The recorded messages older than the oldest one on show, newest first, for the log's "Load older" row. */
export function olderThan(history: RecordedMessage[], shown: { at: string }[]): RecordedMessage[] {
  const oldest = shown.reduce((a, m) => (m.at < a ? m.at : a), "￿");
  return history.filter((m) => m.at < oldest).sort((a, b) => b.at.localeCompare(a.at));
}
