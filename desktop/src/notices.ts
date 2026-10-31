// The native notifications shown for what waits on the owner, by the item's key: a newer one for the
// same key replaces it, and withdraw closes those whose items were answered (anywhere).
export interface Closable {
  close(): void;
}

export function notices<N extends Closable>(): { add(key: string, n: N): void; withdraw(keys: string[]): void; size(): number } {
  const byKey = new Map<string, N>();
  return {
    add(key, n) {
      if (key === "") return;
      byKey.get(key)?.close();
      byKey.set(key, n);
    },
    withdraw(keys) {
      for (const k of keys) {
        byKey.get(k)?.close();
        byKey.delete(k);
      }
    },
    size: () => byKey.size,
  };
}
