#!/usr/bin/env python3
"""Tests of tools/sync_labels.py (the labels.yml parser and the idempotent create/update pass).

Run them from the module root (`make tools-test`; CI's `lint` job calls it):

  python3 -m unittest tools/test_sync_labels.py

No network and no `gh`: the repository's labels come from an injected runner.
"""
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import sync_labels

LABELS_FILE = Path(__file__).resolve().parent.parent / ".github" / "labels.yml"


class ParseTest(unittest.TestCase):
    def test_parses_the_documented_shape(self):
        labels = sync_labels.parse_labels(
            "# a comment\n"
            "- name: bug\n"
            '  color: "d73a4a"\n'
            "  description: \"Something isn't working\"\n"
            "- name: daemon\n"
            "  color: 1d76db\n"
            "  description: \"Area: the daemon\"  # trailing note\n"
        )
        self.assertEqual([
            {"name": "bug", "color": "d73a4a", "description": "Something isn't working"},
            {"name": "daemon", "color": "1d76db", "description": "Area: the daemon"},
        ], labels)

    def test_the_repository_file(self):
        labels = sync_labels.parse_labels(LABELS_FILE.read_text(encoding="utf8"))
        names = [label["name"] for label in labels]
        self.assertEqual(len(names), len(set(names)))
        for expected in ("bug", "documentation", "enhancement", "dungeon", "dashboard", "commander", "addon",
                         "movement", "combat", "guild", "gear", "stability", "daemon", "player-actions", "raid",
                         "battleground", "plans", "testing", "bots", "ci", "data", "live-check", "parity"):
            self.assertIn(expected, names)
        # The people-owned labels are never in the file, so the script cannot touch them.
        for people in ("Must have", "Should have", "Nice to have", "Maybe later", "TortoiseBots"):
            self.assertNotIn(people, names)

    def test_refuses_a_missing_color(self):
        with self.assertRaises(ValueError):
            sync_labels.parse_labels("- name: x\n  description: d\n")

    def test_refuses_a_description_over_100_characters(self):
        with self.assertRaises(ValueError):
            sync_labels.parse_labels("- name: x\n  color: 1d76db\n  description: \"" + "d" * 101 + "\"\n")

    def test_refuses_a_duplicate(self):
        with self.assertRaises(ValueError):
            sync_labels.parse_labels("- name: x\n  color: 1d76db\n  description: d\n"
                                     "- name: x\n  color: 1d76db\n  description: d\n")

    def test_refuses_an_unknown_line(self):
        with self.assertRaises(ValueError):
            sync_labels.parse_labels("- name: x\n  color: 1d76db\n  description: d\n  labels: y\n")


class SyncTest(unittest.TestCase):
    def _runner(self, existing):
        calls = []

        def runner(args):
            calls.append(args)
            if args[:2] == ["label", "list"]:
                return json.dumps(existing)
            if args[:2] in (["label", "create"], ["label", "edit"]):
                return ""
            raise AssertionError("unexpected gh call: %r" % (args,))

        return runner, calls

    def test_dry_run_creates_and_updates_nothing(self):
        labels = [{"name": "daemon", "color": "1d76db", "description": "Area: the daemon"},
                  {"name": "raid", "color": "1d76db", "description": "Area: raids"}]
        runner, calls = self._runner([{"name": "raid", "color": "000000", "description": "old"}])
        actions, created, updated = sync_labels.sync(labels, "o/r", True, runner)
        self.assertEqual(["create label daemon (Area: the daemon)", "update label raid (Area: raids)"], actions)
        self.assertEqual((1, 1), (created, updated))
        self.assertEqual(1, len(calls))

    def test_applies_create_and_edit(self):
        labels = [{"name": "daemon", "color": "1d76db", "description": "Area: the daemon"},
                  {"name": "raid", "color": "1d76db", "description": "Area: raids"}]
        runner, calls = self._runner([{"name": "raid", "color": "#000000", "description": "old"}])
        sync_labels.sync(labels, "o/r", False, runner)
        edits = [c for c in calls if c[:2] != ["label", "list"]]
        self.assertEqual([
            ["label", "create", "daemon", "--repo", "o/r", "--color", "1d76db",
             "--description", "Area: the daemon"],
            ["label", "edit", "raid", "--repo", "o/r", "--color", "1d76db", "--description", "Area: raids"],
        ], edits)

    def test_unchanged_labels_are_left_alone(self):
        labels = [{"name": "raid", "color": "1d76db", "description": "Area: raids"}]
        runner, calls = self._runner([{"name": "raid", "color": "1D76DB", "description": "Area: raids"}])
        actions, created, updated = sync_labels.sync(labels, "o/r", False, runner)
        self.assertEqual([], actions)
        self.assertEqual((0, 0), (created, updated))
        self.assertEqual(1, len(calls))

    def test_never_deletes_or_renames(self):
        labels = [{"name": "raid", "color": "1d76db", "description": "Area: raids"}]
        runner, calls = self._runner([])
        sync_labels.sync(labels, "o/r", False, runner)
        for call in calls:
            self.assertNotIn("delete", call)
            self.assertNotIn("rename", call)


if __name__ == "__main__":
    unittest.main()
