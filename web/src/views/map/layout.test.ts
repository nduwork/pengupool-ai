import { describe, expect, it } from "vitest";

import { layout, rounded, type TreeNode } from "./layout";

const S = { sib: 12, rank: 36, stack: 8, indent: 24 };
const leaf = (id: string): TreeNode => ({ id, children: [] });

function overlaps(a: { x: number; y: number; w: number; h: number }, b: { x: number; y: number; w: number; h: number }): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

describe("map layouts", () => {
  const tree: TreeNode = { id: "m", children: [{ id: "a", children: [leaf("a1"), leaf("a2")] }, leaf("b")] };
  // Cards size to their content: one with a workflow chain is wider and taller than one without.
  const mixed = (id: string): { w: number; h: number } => (id === "a" || id === "b" ? { w: 260, h: 102 } : { w: 180, h: 60 });
  for (const [kind, size] of (["org", "outline", "right"] as const).flatMap((k) => [[k, () => ({ w: 180, h: 60 })] as const, [k, mixed] as const])) {
    it(`${kind}${size === mixed ? " (mixed sizes)" : ""}: no two cards overlap and every edge is right-angled`, () => {
      const b = layout(kind, [tree], size, S);
      const cards = Object.values(b.cards);
      expect(cards).toHaveLength(5);
      for (let i = 0; i < cards.length; i++) for (let j = i + 1; j < cards.length; j++) expect(overlaps(cards[i], cards[j])).toBe(false);
      for (const r of b.routes) {
        for (let k = 1; k < r.points.length; k++) {
          const [p, q] = [r.points[k - 1], r.points[k]];
          expect(p[0] === q[0] || p[1] === q[1]).toBe(true);
        }
      }
      expect(b.routes).toHaveLength(4);
      for (const [id, c] of Object.entries(b.cards)) expect([c.w, c.h]).toEqual([size(id).w, size(id).h]);
    });
  }
  it("org stacks three or more leaf children on a spine", () => {
    const b = layout("org", [{ id: "m", children: [leaf("a"), leaf("b"), leaf("c")] }], () => ({ w: 180, h: 60 }), S);
    expect(b.cards.a.x).toBe(b.cards.b.x);
    expect(b.cards.b.y).toBeGreaterThan(b.cards.a.y);
  });
  it("rounds bends", () => {
    expect(
      rounded([
        [0, 0],
        [0, 10],
        [10, 10],
      ]),
    ).toContain("Q0,10");
  });
});
