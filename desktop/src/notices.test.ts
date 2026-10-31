// node --test src/notices.test.ts (npm test)
import assert from "node:assert/strict";
import { test } from "node:test";

import { notices } from "./notices.ts";

test("a notification is withdrawn once its item is answered, and replaced by a newer one for its key", () => {
  const closed: string[] = [];
  const n = (id: string) => ({ close: () => closed.push(id) });
  const s = notices<ReturnType<typeof n>>();
  s.add("need:a:permission", n("first"));
  s.add("need:a:permission", n("second")); // the same item again: the old one goes
  s.add("approval:L", n("launch"));
  s.add("", n("unkeyed")); // an older app sends no key: nothing to withdraw later
  assert.deepEqual(closed, ["first"]);
  s.withdraw(["need:a:permission", "unknown"]);
  assert.deepEqual(closed, ["first", "second"]);
  assert.equal(s.size(), 1);
});
