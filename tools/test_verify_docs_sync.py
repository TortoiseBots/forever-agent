#!/usr/bin/env python3
"""Tests for tools/verify_docs_sync.py.

Run with: python3 -m unittest tools/test_verify_docs_sync.py

The decision is pure: lists of changed paths, added diff lines and a pull request body in, problems out.
"""

import contextlib
import io
import os
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import verify_docs_sync  # noqa: E402  (the module under test sits next to this file)


def decide(paths, added=(), body=""):
    return verify_docs_sync.decide(paths, list(added), body)


class DecideTest(unittest.TestCase):
    def test_a_change_with_no_trigger_passes(self):
        self.assertEqual(decide(["internal/navmesh/route.go", "docs/CI.md"]), [])

    def test_a_cli_change_needs_a_doc(self):
        problems = decide(["cmd/forever-agent/forever_map.go"])
        self.assertEqual(len(problems), 1)
        self.assertIn("docs/manual/", problems[0])
        self.assertIn("DOCS-SYNC.md", problems[0])
        self.assertIn("Docs: not needed", problems[0])

    def test_a_cli_change_with_a_manual_page_passes(self):
        self.assertEqual(
            decide(["cmd/forever-agent/forever_map.go", "docs/manual/daemon/travel.md"]), []
        )

    def test_a_cli_change_with_the_readme_passes(self):
        self.assertEqual(decide(["cmd/forever-agent/main.go", "README.md"]), [])

    def test_a_serve_method_file_is_a_trigger(self):
        problems = decide(["internal/forever/world/serve_mail_methods.go"])
        self.assertIn("serve's method table", problems[0])

    def test_another_serve_file_is_not_a_trigger(self):
        self.assertEqual(decide(["internal/forever/world/serve_session.go"]), [])

    def test_a_test_file_under_cmd_is_a_trigger(self):
        # The CLI rule is by path: a test that pins behaviour is still a CLI change.
        self.assertEqual(len(decide(["cmd/forever-agent/forever_map_test.go"])), 1)

    def test_an_added_flag_is_a_trigger(self):
        added = ['\tstay := fs.Duration("stay", 60*time.Second, "how long to stay")']
        problems = decide(["internal/forever/world/route_walk.go"], added)
        self.assertIn("a new flag", problems[0])

    def test_an_added_flag_with_a_doc_passes(self):
        added = ['\tstay := fs.Duration("stay", 60*time.Second, "how long to stay")']
        self.assertEqual(decide(["README.md"], added), [])

    def test_a_line_that_only_mentions_a_flag_call_is_not_a_trigger(self):
        added = ["// fs.String names a flag; this is prose in a comment"]
        self.assertEqual(decide(["docs/PLANS.md"], added), [])

    def test_the_waiver_line_opts_out(self):
        body = "What changed\n\nDocs: not needed, because the flag only renames a local variable.\n"
        self.assertEqual(decide(["cmd/forever-agent/forever_map.go"], body=body), [])

    def test_the_waiver_must_start_the_line(self):
        body = "We wrote docs: not needed in the description of something else.\n"
        self.assertEqual(len(decide(["cmd/forever-agent/forever_map.go"], body=body)), 1)

    def test_the_waiver_needs_a_reason(self):
        self.assertEqual(len(decide(["cmd/forever-agent/forever_map.go"], body="Docs: not needed\n")), 1)
        self.assertEqual(
            len(decide(["cmd/forever-agent/forever_map.go"], body="Docs: not needed, because\n")), 1
        )

    def test_a_waiver_inside_a_fenced_block_does_not_count(self):
        body = "What changed\n\n```\nDocs: not needed, because the example is only text.\n```\n"
        self.assertEqual(len(decide(["cmd/forever-agent/forever_map.go"], body=body)), 1)

    def test_a_flag_on_another_flag_set_is_a_trigger(self):
        added = ['\tstay := flags.Duration("stay", 60*time.Second, "how long to stay")']
        self.assertIn("a new flag", decide(["internal/forever/world/route_walk.go"], added)[0])

    def test_the_flag_package_itself_is_a_trigger(self):
        added = ['fs := flag.NewFlagSet("check", flag.ContinueOnError)']
        self.assertIn("a new flag", decide(["cmd/tool/main.go"], added)[0])

    def test_an_added_register_call_is_a_trigger(self):
        added = ['srv.Register("bank_open", nil)']
        self.assertIn("a new flag", decide(["internal/forever/world/bank_open.go"], added)[0])

    def test_a_comment_that_names_register_is_not_a_trigger(self):
        added = ["// srv.Register is where the method table grows; this is prose in a comment"]
        self.assertEqual(decide(["docs/PLANS.md"], added), [])

    def test_a_doc_change_outside_the_manual_does_not_count(self):
        self.assertEqual(len(decide(["cmd/forever-agent/main.go", "docs/CI.md"])), 1)

    def test_added_lines_strip_the_diff_markers(self):
        diff = (
            "--- a/x.go\n"
            "+++ b/x.go\n"
            "@@ -1,2 +1,3 @@\n"
            " old\n"
            '+fs.Bool("verbose", false, "more output")\n'
            "-gone\n"
        )
        self.assertEqual(
            verify_docs_sync.added_lines(diff), ['fs.Bool("verbose", false, "more output")']
        )


class MainTest(unittest.TestCase):
    def run_main(self, changed, diff, body):
        with tempfile.TemporaryDirectory() as tmp:
            changed_file = os.path.join(tmp, "changed.txt")
            diff_file = os.path.join(tmp, "change.diff")
            with open(changed_file, "w", encoding="utf-8") as handle:
                handle.write(changed)
            with open(diff_file, "w", encoding="utf-8") as handle:
                handle.write(diff)
            with mock.patch.dict(os.environ, {"PR_BODY": body}):
                with contextlib.redirect_stdout(io.StringIO()) as out:
                    code = verify_docs_sync.main(
                        [
                            "verify_docs_sync.py",
                            "--changed-files",
                            changed_file,
                            "--diff",
                            diff_file,
                        ]
                    )
                self.last_output = out.getvalue()
                return code

    def test_a_clean_change_exits_zero(self):
        self.assertEqual(self.run_main("docs/manual/log.md\n", "", ""), 0)

    def test_a_trigger_without_docs_exits_one(self):
        self.assertEqual(self.run_main("cmd/forever-agent/main.go\n", "", ""), 1)

    def test_the_waiver_from_the_environment_passes(self):
        self.assertEqual(
            self.run_main("cmd/forever-agent/main.go\n", "", "Docs: not needed, because a rename only\n"), 0
        )

    def test_the_added_lines_come_from_the_diff(self):
        diff = "+++ b/x.go\n+fs.Int(\"count\", 1, \"how many\")\n"
        self.assertEqual(self.run_main("internal/forever/world/route_walk.go\n", diff, ""), 1)


if __name__ == "__main__":
    unittest.main()
