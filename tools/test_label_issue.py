#!/usr/bin/env python3
"""Tests of the automatic labelling scripts (tools/label_issue.py, tools/sync_labels.py).

Run them from the module root (they are not part of `make test`):

  python3 -m unittest tools/test_label_issue.py

No network and no `gh`: every command goes through an injected runner, and the label files are the real ones in this
tree.
"""
import contextlib
import io
import json
import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import label_issue
import sync_labels

ROOT = Path(__file__).resolve().parent.parent
LABELS_FILE = ROOT / ".github" / "labels.yml"

# One phrase per rule, from the brief's examples, to prove each rule fires.
RULE_SAMPLES = (
    ("dungeon", "dungeon run stopped at the first pull"),
    ("dungeon", "dungeon-play did not enter Ragefire Chasm"),
    ("dungeon", "dungeon-watch shows nothing"),
    ("dungeon", "the Wailing Caverns walk desynced"),
    ("raid", "the raid wiped on the first boss"),
    ("raid", "raids are missing from the suite"),
    ("battleground", "bg-watch never joined"),
    ("battleground", "the battleground queue is empty"),
    ("plans", "the plan for RFC is stale"),
    ("plans", "plans need a new route"),
    ("plans", "navmesh files are missing"),
    ("daemon", "the daemon forgot the session"),
    ("daemon", "serve answers nothing"),
    ("daemon", "the rpc method returned an error"),
    ("daemon", "a method is missing"),
    ("dashboard", "the dashboard is empty"),
    ("addon", "the add-on lost the chat"),
    ("addon", "the addon shows no buttons"),
    ("addon", "TBM says the wrong thing"),
    ("testing", "the bench numbers are off"),
    ("testing", "a trial run failed"),
    ("testing", "spell-score is wrong"),
    ("testing", "fight-score regressed"),
    ("testing", "bug-hunter finds nothing"),
    ("player-actions", "the bank slot is lost"),
    ("player-actions", "mail never arrives"),
    ("player-actions", "the auction listing is gone"),
    ("player-actions", "trade leaves the bot stuck"),
    ("player-actions", "the vendor refuses the sale"),
    ("player-actions", "the quest is not accepted"),
    ("player-actions", "loot is skipped"),
    ("player-actions", "the trainer teaches nothing"),
    ("player-actions", "the taxi never takes off"),
    ("live-check", "live check at 21:00 failed"),
    ("live-check", "the live session froze"),
    ("live-check", "live run 3 stopped"),
    ("parity", "the parity re-score differs"),
    ("parity", "parity is off by two"),
    ("documentation", "the docs are stale"),
    ("documentation", "documentation misses the flag"),
    ("documentation", "README names the old command"),
    ("documentation", "the manual is wrong"),
)


class RulesTest(unittest.TestCase):
    def test_each_rule_fires(self):
        for label, text in RULE_SAMPLES:
            with self.subTest(label=label, text=text):
                self.assertIn(label, label_issue.labels_for_text(text, ""))

    def test_rules_read_the_body_too(self):
        self.assertEqual(["raid"], label_issue.labels_for_text("nothing here", "the raid wiped"))

    def test_word_boundaries(self):
        for text in ("braid", "the braided path", "emails", "mailbox", "servers", "planter", "upgrade nothing"):
            with self.subTest(text=text):
                self.assertEqual([], label_issue.labels_for_text(text, ""))

    def test_case_insensitive(self):
        self.assertEqual(["raid"], label_issue.labels_for_text("THE RAID", ""))

    def test_no_label_twice(self):
        labels = label_issue.labels_for_text("raid RAID Raid", "raid, raids, raid")
        self.assertEqual(["raid"], labels)

    def test_bug_word_in_title(self):
        for title in ("Crash on login", "The walk fails", "fix the taxi", "guild bank is broken",
                      "login refused", "Agent: crash in combat"):
            with self.subTest(title=title):
                self.assertIn("bug", label_issue.labels_for_text(title, ""))

    def test_bug_word_must_be_a_whole_word(self):
        self.assertNotIn("bug", label_issue.labels_for_text("Agent: prefix handling", ""))

    def test_agent_title_without_a_bug_word_is_an_enhancement(self):
        for title in ("Agent: add taxi support", "[agent] teach the bot to bank", "  Agent: new dashboard panel"):
            with self.subTest(title=title):
                labels = label_issue.labels_for_text(title, "")
                self.assertIn("enhancement", labels)
                self.assertNotIn("bug", labels)

    def test_bug_word_in_the_body_does_not_make_a_bug(self):
        labels = label_issue.labels_for_text("Agent: replay the walk", "the previous fix helped")
        self.assertIn("enhancement", labels)
        self.assertNotIn("bug", labels)

    def test_bug_wins_over_the_agent_title(self):
        labels = label_issue.labels_for_text("Agent: fix the dungeon run", "")
        self.assertIn("bug", labels)
        self.assertNotIn("enhancement", labels)

    def test_plain_title_gets_no_status_label(self):
        self.assertEqual([], label_issue.labels_for_text("Slots on the paperdoll", ""))

    def test_hostile_text_is_only_text(self):
        marker = Path("/tmp/fa-label-test-pwned")
        if marker.exists():
            marker.unlink()
        title = '`touch %s` $(touch %s) ; touch %s | "<script>" \\ $HOME' % (marker, marker, marker)
        body = "'; DROP TABLE labels; --\n" + "x" * 1_000_000 + "\nraid"
        labels = label_issue.labels_for_text(title, body)
        self.assertEqual(["raid"], labels)
        self.assertFalse(marker.exists())

    def test_very_long_body_with_a_rule(self):
        body = "raid " * 20_000
        self.assertEqual(["raid"], label_issue.labels_for_text("", body))

    def test_every_rule_is_a_known_label(self):
        owned = {label["name"] for label in sync_labels.parse_labels(LABELS_FILE.read_text(encoding="utf8"))}
        for label, _ in label_issue.RULES:
            self.assertIn(label, owned)
        self.assertTrue({"bug", "enhancement"} <= owned)


class LabelerFileTest(unittest.TestCase):
    def setUp(self):
        self.rules = label_issue.parse_labeler(label_issue.LABELER_FILE.read_text(encoding="utf8"))

    def test_branch_rules(self):
        expected = {"bug": "^fix/", "enhancement": "^feat/", "documentation": "^docs/", "data": "^data/",
                    "ci": "^ci/"}
        for label, pattern in expected.items():
            with self.subTest(label=label):
                self.assertEqual([pattern], self.rules[label]["branches"])

    def test_path_rules(self):
        self.assertIn("internal/forever/world/serve*", self.rules["daemon"]["globs"])
        self.assertIn("internal/rpc/**", self.rules["daemon"]["globs"])
        self.assertIn("internal/forever/**/*_act.go", self.rules["player-actions"]["globs"])
        self.assertIn("dungeons/**", self.rules["plans"]["globs"])
        self.assertIn("**/raid*.go", self.rules["raid"]["globs"])
        self.assertIn("**/*.md", self.rules["documentation"]["globs"])

    def test_every_labeler_label_is_owned(self):
        owned = {label["name"] for label in sync_labels.parse_labels(LABELS_FILE.read_text(encoding="utf8"))}
        self.assertTrue(set(self.rules) <= owned)
        self.assertNotIn("TortoiseBots", owned)
        self.assertNotIn("Must have", owned)

    def test_changed_files_to_labels(self):
        cases = (
            ("internal/rpc/client.go", {"daemon"}),
            ("internal/forever/world/serve.go", {"daemon"}),
            ("internal/forever/serveclient/daemon.go", {"daemon"}),
            ("internal/forever/world/quest_act.go", {"player-actions"}),
            ("internal/forever/mail/mail.go", {"player-actions"}),
            ("internal/dungeon/run.go", {"dungeon"}),
            ("dungeons/ragefire.json", {"plans"}),
            ("dungeons/sub/ragefire.json", {"plans"}),
            ("internal/planlint/lint.go", {"plans"}),
            ("internal/routeoracle/check.go", {"plans"}),
            ("internal/patrols/patrols.go", {"plans"}),
            ("tools/dungeongen/main.go", {"plans"}),
            ("internal/raidwalk/walk.go", {"raid"}),
            ("cmd/forever-agent/raid_walk_cmd.go", {"raid"}),
            ("internal/forever/bg/bg.go", {"battleground"}),
            ("internal/forever/pbc/pbc.go", {"bots"}),
            ("contract/tbm/v1/MANIFEST", {"bots"}),
            ("internal/forever/cmdctl/ctl.go", {"bots", "commander"}),
            ("internal/forever/tbmproto/msg.go", {"addon"}),
            ("internal/addonhost/host.go", {"addon"}),
            ("internal/tbdash/dash.go", {"dashboard"}),
            ("internal/navmesh/mesh.go", {"movement"}),
            ("internal/forever/objects/mover.go", {"movement"}),
            ("internal/forever/spelljudge/judge.go", {"combat"}),
            ("internal/kbspells/oracle.go", {"combat"}),
            ("internal/forever/guild/guild.go", {"guild"}),
            ("internal/bughunt/hunt.go", {"testing"}),
            ("internal/tracerun/run.go", {"testing"}),
            ("scenarios/raid.json", {"testing"}),
            ("data/items.json", {"data"}),
            ("docs/CI.md", {"documentation"}),
            ("README.md", {"documentation"}),
            (".github/workflows/test.yml", {"ci"}),
            ("Makefile", {"ci"}),
            ("scripts/walk.sh", {"ci"}),
        )
        for path, expected in cases:
            with self.subTest(path=path):
                self.assertEqual(expected, set(label_issue.labels_for_files(self.rules, [path])))

    def test_unrelated_paths_get_nothing(self):
        self.assertEqual([], label_issue.labels_for_files(self.rules, ["internal/rpcx/client.go",
                                                                      "internal/dashboardx/x.go",
                                                                      "docs2/x.go",
                                                                      "internal/forever/party/x.go"]))

    def test_glob_vocabulary(self):
        cases = (
            ("internal/forever/world/serve*", "internal/forever/world/serve.go", True),
            ("internal/forever/world/serve*", "internal/forever/world/serve/sub/x.go", False),
            ("**/*.md", "README.md", True),
            ("**/*.md", "docs/adr/0001.md", True),
            ("**/raid*.go", "cmd/forever-agent/raid_walk_cmd.go", True),
            ("**/raid*.go", "cmd/forever-agent/walk_raid.go", False),
            ("internal/rpc/**", "internal/rpc/x.go", True),
            ("internal/rpc/**", "internal/rpcx/x.go", False),
            ("dungeons/**", "dungeons/a/b/c.json", True),
            ("data/**", "data/x.json", True),
            ("data/**", "databases/x.json", False),
        )
        for pattern, path, expected in cases:
            with self.subTest(pattern=pattern, path=path):
                self.assertEqual(expected, bool(label_issue.glob_regex(pattern).match(path)))

    def test_parse_rejects_a_bad_shape(self):
        with self.assertRaises(ValueError):
            label_issue.parse_labeler("a label with no colon\n")
        with self.assertRaises(ValueError):
            label_issue.parse_labeler("x:\n  - a value before any matcher\n")


def fake_gh(calls):
    """A gh stub: the open issues, two pull requests, and the edit calls recorded in `calls`."""

    def runner(args):
        calls.append(args)
        if args[:2] == ["issue", "list"]:
            return json.dumps([
                {"number": 7, "title": "dungeon run broken", "body": "", "labels": []},
                {"number": 8, "title": "nothing at all", "body": "", "labels": []},
            ])
        if args[:2] == ["pr", "list"]:
            return json.dumps([{"number": 9}, {"number": 10}])
        if args[:2] == ["pr", "view"] and args[2] == "9":
            return json.dumps({"files": [{"path": "internal/rpc/x.go"}, {"path": "README.md"}], "labels": []})
        if args[:2] == ["pr", "view"] and args[2] == "10":
            return json.dumps({"files": [{"path": "internal/rpc/y.go"}], "labels": [{"name": "daemon"}]})
        if args[:2] == ["api", "--method"]:
            return ""
        raise AssertionError("unexpected gh call: %r" % (args,))

    return runner


class BackfillTest(unittest.TestCase):
    def test_dry_run_prints_and_changes_nothing(self):
        calls = []
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            items, added = label_issue.backfill("o/r", 500, True, fake_gh(calls))
        self.assertEqual((2, 4), (items, added))
        self.assertFalse([c for c in calls if c[:2] == ["api", "--method"]])
        lines = out.getvalue().splitlines()
        self.assertEqual(["issue 7: +dungeon,bug", "pr 9: +documentation,daemon",
                          "dry-run: 2 issues and pull requests, 4 labels"], lines)

    def test_applies_only_the_missing_labels(self):
        calls = []
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            items, added = label_issue.backfill("o/r", 500, False, fake_gh(calls))
        self.assertEqual((2, 4), (items, added))
        edits = [c for c in calls if c[:2] == ["api", "--method"]]
        self.assertEqual([
            ["api", "--method", "POST", "repos/o/r/issues/7/labels",
             "-f", "labels[]=dungeon", "-f", "labels[]=bug"],
            ["api", "--method", "POST", "repos/o/r/issues/9/labels",
             "-f", "labels[]=documentation", "-f", "labels[]=daemon"],
        ], edits)

    def test_nothing_to_do(self):
        calls = []

        def runner(args):
            calls.append(args)
            if args[:2] == ["issue", "list"]:
                return "[]"
            if args[:2] == ["pr", "list"]:
                return "[]"
            raise AssertionError(args)

        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual((0, 0), label_issue.backfill("o/r", 500, False, runner))


class EnvModeTest(unittest.TestCase):
    def tearDown(self):
        for key in ("ISSUE_NUMBER", "ISSUE_TITLE", "ISSUE_BODY"):
            os.environ.pop(key, None)

    def test_labels_the_issue_from_the_environment(self):
        os.environ.update(ISSUE_NUMBER="12", ISSUE_TITLE="Ragefire Chasm crash", ISSUE_BODY="the daemon")
        calls = []

        def runner(args):
            calls.append(args)
            return ""

        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            label_issue.label_env_issue("o/r", False, runner)
        self.assertEqual(["api", "--method", "POST", "repos/o/r/issues/12/labels",
                          "-f", "labels[]=dungeon", "-f", "labels[]=daemon", "-f", "labels[]=bug"], calls[0])
        self.assertEqual("issue 12: +dungeon,daemon,bug", out.getvalue().strip())

    def test_dry_run_makes_no_call(self):
        os.environ.update(ISSUE_NUMBER="13", ISSUE_TITLE="raid", ISSUE_BODY="")
        calls = []
        with contextlib.redirect_stdout(io.StringIO()):
            label_issue.label_env_issue("o/r", True, calls.append)
        self.assertEqual([], calls)

    def test_a_number_is_required(self):
        os.environ.pop("ISSUE_NUMBER", None)
        with self.assertRaises(SystemExit):
            label_issue.label_env_issue("o/r", True, lambda args: "")


if __name__ == "__main__":
    unittest.main()
