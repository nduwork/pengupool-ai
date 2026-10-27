// Tree layouts for one group on the map, as v1 drew them: every line leaves its parent, turns at
// right angles on a bus or spine, and enters the child; none runs diagonally or behind a card.
// Coordinates are top-left. Each card has its own size (v1 measured them), so a card with a workflow
// chain is bigger than one without; children of one parent share a rank and top-align.

export interface TreeNode {
  id: string;
  children: TreeNode[];
}

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Route {
  from: string;
  to: string;
  points: Array<[number, number]>;
}

export interface Block {
  cards: Record<string, Box>;
  routes: Route[];
  w: number;
  h: number;
}

export interface Spacing {
  sib: number; // between siblings
  rank: number; // between a parent and its children
  stack: number; // between stacked cards
  indent: number; // outline and spine indent
}

export type LayoutKind = "org" | "outline" | "right";

/** A card's width and height, by session id. */
export type Size = (id: string) => { w: number; h: number };

function shift(b: Block, dx: number, dy: number): Block {
  const cards: Record<string, Box> = {};
  for (const [k, c] of Object.entries(b.cards)) cards[k] = { ...c, x: c.x + dx, y: c.y + dy };
  return { cards, routes: b.routes.map((r) => ({ ...r, points: r.points.map(([x, y]) => [x + dx, y + dy] as [number, number]) })), w: b.w, h: b.h };
}

function merge(into: Block, b: Block): void {
  Object.assign(into.cards, b.cards);
  into.routes.push(...b.routes);
}

/** Org chart: subtrees side by side under their parent; three or more leaf children stack in a
 *  column on a spine, so a wide fan never stretches the map. */
export function org(n: TreeNode, size: Size, S: Spacing): Block {
  const me = size(n.id);
  const block: Block = { cards: { [n.id]: { x: 0, y: 0, ...me } }, routes: [], w: me.w, h: me.h };
  if (n.children.length === 0) return block;
  const leaves = n.children.every((c) => c.children.length === 0);
  if (leaves && n.children.length >= 3) {
    const spineX = S.indent / 2;
    let y = me.h + S.rank / 2;
    for (const c of n.children) {
      const cs = size(c.id);
      block.cards[c.id] = { x: S.indent, y, ...cs };
      block.routes.push({
        from: n.id,
        to: c.id,
        points: [
          [spineX, me.h],
          [spineX, y + cs.h / 2],
          [S.indent, y + cs.h / 2],
        ],
      });
      block.w = Math.max(block.w, S.indent + cs.w);
      y += cs.h + S.stack;
    }
    block.h = y - S.stack;
    return block;
  }
  const subs = n.children.map((c) => org(c, size, S));
  const total = subs.reduce((a, b) => a + b.w, 0) + S.sib * (subs.length - 1);
  const W = Math.max(me.w, total);
  const px = (W - me.w) / 2;
  block.cards[n.id] = { x: px, y: 0, ...me };
  const childY = me.h + S.rank;
  const bus = me.h + S.rank / 2;
  let x = (W - total) / 2;
  let H = me.h;
  subs.forEach((sb, i) => {
    const placed = shift(sb, x, childY);
    merge(block, placed);
    const child = placed.cards[n.children[i].id];
    const cx = child.x + child.w / 2;
    block.routes.push({
      from: n.id,
      to: n.children[i].id,
      points: [
        [px + me.w / 2, me.h],
        [px + me.w / 2, bus],
        [cx, bus],
        [cx, childY],
      ],
    });
    H = Math.max(H, childY + sb.h);
    x += sb.w + S.sib;
  });
  block.w = W;
  block.h = H;
  return block;
}

/** Outline: one column, each child indented under its parent and hung off its spine. */
export function outline(n: TreeNode, size: Size, S: Spacing): Block {
  const block: Block = { cards: {}, routes: [], w: 0, h: 0 };
  let y = 0;
  const walk = (node: TreeNode, depth: number): void => {
    const x = depth * S.indent;
    const me = size(node.id);
    block.cards[node.id] = { x, y, ...me };
    block.w = Math.max(block.w, x + me.w);
    y += me.h + S.stack;
    for (const c of node.children) {
      const cy = y;
      walk(c, depth + 1);
      const sx = x + S.indent / 2;
      const ch = block.cards[c.id].h;
      block.routes.push({
        from: node.id,
        to: c.id,
        points: [
          [sx, block.cards[node.id].y + me.h],
          [sx, cy + ch / 2],
          [x + S.indent, cy + ch / 2],
        ],
      });
    }
  };
  walk(n, 0);
  block.h = y - S.stack;
  return block;
}

/** Tree →: the org chart turned on its side (children to the right of their parent). */
export function right(n: TreeNode, size: Size, S: Spacing): Block {
  const t = org(
    n,
    (id) => {
      const s = size(id);
      return { w: s.h, h: s.w };
    },
    { ...S, rank: S.rank * 1.6 },
  );
  const cards: Record<string, Box> = {};
  for (const [k, c] of Object.entries(t.cards)) cards[k] = { x: c.y, y: c.x, w: c.h, h: c.w };
  return {
    cards,
    routes: t.routes.map((r) => ({ ...r, points: r.points.map(([x, y]) => [y, x] as [number, number]) })),
    w: t.h,
    h: t.w,
  };
}

export function layout(kind: LayoutKind, roots: TreeNode[], size: Size, S: Spacing): Block {
  const fn = kind === "outline" ? outline : kind === "right" ? right : org;
  const out: Block = { cards: {}, routes: [], w: 0, h: 0 };
  // Several roots (a team whose members lost their parent) sit side by side.
  let x = 0;
  for (const r of roots) {
    const b = shift(fn(r, size, S), x, 0);
    merge(out, b);
    out.w = x + b.w;
    out.h = Math.max(out.h, b.h);
    x += b.w + S.sib * 2;
  }
  return out;
}

/** An SVG path through right-angle points with each bend rounded (r = 6, less on short runs). */
export function rounded(pts: Array<[number, number]>): string {
  const p = pts.filter((q, i) => i === 0 || q[0] !== pts[i - 1][0] || q[1] !== pts[i - 1][1]);
  if (p.length === 0) return "";
  const toward = (a: [number, number], b: [number, number], r: number): [number, number] => {
    const d = Math.hypot(b[0] - a[0], b[1] - a[1]) || 1;
    return [a[0] + ((b[0] - a[0]) * r) / d, a[1] + ((b[1] - a[1]) * r) / d];
  };
  let d = `M${p[0][0]},${p[0][1]}`;
  for (let i = 1; i < p.length - 1; i++) {
    const [a, c, n] = [p[i - 1], p[i], p[i + 1]];
    const r = Math.min(6, Math.hypot(c[0] - a[0], c[1] - a[1]) / 2, Math.hypot(n[0] - c[0], n[1] - c[1]) / 2);
    const s = toward(c, a, r);
    const e = toward(c, n, r);
    d += ` L${s[0]},${s[1]} Q${c[0]},${c[1]} ${e[0]},${e[1]}`;
  }
  const e = p[p.length - 1];
  return `${d} L${e[0]},${e[1]}`;
}
