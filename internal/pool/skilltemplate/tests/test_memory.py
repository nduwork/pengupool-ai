"""Smoke tests for the MEMORY.md editor: create, compare-and-swap, verify, update (stdlib unittest)."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "skills", "{{task}}", "scripts", "memory.py")
P1 = '- [P1] be terse — evidence: "be concise" — added 2026-01-01'


def run(*args, stdin=None):
    return subprocess.run([sys.executable, SCRIPT, *args], input=stdin, capture_output=True, text=True)


class MemoryTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.dir.name, "MEMORY.md")

    def tearDown(self):
        self.dir.cleanup()

    def apply(self, proposal, expect=None):
        expect = expect or run("hash", self.path).stdout.strip()
        return run("apply", self.path, "--expect", expect, "--proposal", "-", stdin=json.dumps(proposal))

    def test_create_requires_owner(self):
        self.assertEqual(self.apply({"preferences": [P1]}).returncode, 2)
        self.assertFalse(os.path.exists(self.path))

    def test_create_then_entry_present(self):
        self.assertEqual(self.apply({"owner": "tester", "preferences": [P1]}).returncode, 0)
        with open(self.path, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("_Owner: tester_", text)
        self.assertIn(P1, text)

    def test_stale_hash_is_refused(self):
        self.apply({"owner": "tester"})
        r = self.apply({"preferences": [P1]}, expect="0" * 64)
        self.assertEqual(r.returncode, 3)

    def test_owner_mismatch_refused(self):
        self.apply({"owner": "tester"})
        self.assertEqual(self.apply({"owner": "someone", "preferences": [P1]}).returncode, 2)

    def test_update_in_place(self):
        self.apply({"owner": "tester", "preferences": [P1]})
        new = "- [P1] superseded — added 2026-01-02"
        self.assertEqual(self.apply({"update": {"P1": new}}).returncode, 0)
        with open(self.path, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn(new, text)
        self.assertNotIn(P1, text)

    def test_duplicate_or_ambiguous_ids_are_refused(self):
        self.assertEqual(self.apply({"owner": "jane.doe", "preferences": [P1]}).returncode, 0)
        self.assertEqual(self.apply({"preferences": [P1.replace("terse", "brief")]}).returncode, 2)
        p2 = '- [P2] cite sources — evidence: "cite" — added 2026-01-01'
        self.assertEqual(self.apply({"preferences": [p2], "update": {"P2": p2}}).returncode, 2)

    def test_apply_into_a_new_folder_and_through_a_symlink(self):
        deep = os.path.join(self.dir.name, "a", "b", "MEMORY.md")
        r = run("apply", deep, "--expect", run("hash", deep).stdout.strip(), "--proposal", "-",
                stdin=json.dumps({"owner": "jane.doe", "preferences": [P1]}))
        self.assertEqual(r.returncode, 0, r.stderr)
        os.symlink(deep, self.path)
        self.assertEqual(self.apply({"preferences": [P1.replace("[P1]", "[P3]")]}).returncode, 0)
        self.assertTrue(os.path.islink(self.path))
        with open(deep, encoding="utf-8") as fh:
            self.assertIn("[P3]", fh.read())

    def test_a_decision_cannot_masquerade_as_an_entry(self):
        self.assertEqual(self.apply({"owner": "jane.doe", "preferences": [P1]}).returncode, 0)
        self.assertEqual(self.apply({"decision": "[P2] noted"}).returncode, 2)
        self.assertEqual(self.apply({"decision": "2026-01-01 added P1 user"}).returncode, 0)


if __name__ == "__main__":
    unittest.main()
