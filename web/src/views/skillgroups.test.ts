import { describe, expect, it } from "vitest";

import { groupOnMap, groupSkills, onMap } from "./skillgroups";

describe("groupSkills", () => {
  it("sorts groups by name, puts the empty group last, keeps item order", () => {
    const items = [
      { n: "a", g: "" },
      { n: "b", g: "team" },
      { n: "c", g: "code" },
      { n: "d", g: "team" },
    ];
    const r = groupSkills(items, (i) => i.g);
    expect(r.map((x) => x.group)).toEqual(["code", "team", ""]);
    expect(r[1].items.map((i) => i.n)).toEqual(["b", "d"]);
  });
  it("returns nothing for no items", () => expect(groupSkills([], () => "")).toEqual([]));
});

describe("onMap", () => {
  it("keeps only the helpers the owner put on the map", () => {
    const skills = [
      { id: "a", on_map: true },
      { id: "b" },
      { id: "c", on_map: false },
    ];
    expect(onMap(skills).map((s) => s.id)).toEqual(["a"]);
  });
  it("gives a folder all, none or mixed", () => {
    expect(groupOnMap([{ on_map: true }, { on_map: true }])).toBe(true);
    expect(groupOnMap([{}, { on_map: false }])).toBe(false);
    expect(groupOnMap([{ on_map: true }, {}])).toBe("mixed");
  });
});
