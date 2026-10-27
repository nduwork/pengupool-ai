/** Buckets items by group: groups sorted by name, "" (shown as "Ungrouped") last, items in input order. */
export function groupSkills<T>(items: T[], groupOf: (t: T) => string): Array<{ group: string; items: T[] }> {
  const m = new Map<string, T[]>();
  for (const t of items) {
    const g = groupOf(t);
    m.set(g, [...(m.get(g) ?? []), t]);
  }
  return [...m.entries()]
    .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
    .map(([group, items]) => ({ group, items }));
}

/** The map's Helper agent pool: the installed helpers the owner ticked "On the map". */
export function onMap<T extends { on_map?: boolean }>(skills: T[]): T[] {
  return skills.filter((s) => s.on_map === true);
}

/** A folder's "On the map" box: all ticked, none, or some ("mixed"). */
export function groupOnMap(skills: Array<{ on_map?: boolean }>): boolean | "mixed" {
  const n = skills.filter((s) => s.on_map === true).length;
  return n === 0 ? false : n === skills.length ? true : "mixed";
}
