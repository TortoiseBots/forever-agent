#!/usr/bin/env python3
"""Tests for tools/verify_okf.py.

Run with: python3 -m unittest tools/test_verify_okf.py

Each test builds a small bundle in a temporary directory. The first test is a
bundle that must pass; every other test breaks one rule and expects at least
one problem that names it.
"""

import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import verify_okf  # noqa: E402  (the module under test sits next to this file)


# Privacy fixtures are assembled from pieces so that this file does not match
# the tree-wide privacy scan that runs nightly (privacy-scan.yml).
HOME_PATH = "/ho" + "me/alice/project"
LITERAL_VALUE = "hunt" + "er2"
PASSWORD_VALUE = "password=" + LITERAL_VALUE
WINDOWS_PATH = "C:" + "\\Users\\alice"

GOOD_AT = "2026-11-24T20:35:00Z"

# The checker reads these two switches the way it reads cmd/forever-agent's own files.
MAIN_GO = (
    "package main\n"
    "\n"
    "func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {\n"
    '\tswitch args[0] {\n'
    '\tcase "version":\n'
    "\t\treturn exitOK\n"
    '\tcase "lease":\n'
    "\t\treturn leaseCmd(args[1:], stdout, stderr)\n"
    '\tcase "forever":\n'
    "\t\treturn foreverCmd(ctx, args[1:], stdout, stderr)\n"
    "\tdefault:\n"
    "\t\treturn exitUsage\n"
    "\t}\n"
    "\n"
    'const usageText = "usage: forever-agent version"\n'
)

FOREVER_GO = (
    "package main\n"
    "\n"
    "func foreverCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {\n"
    '\tswitch args[0] {\n'
    '\tcase "serve":\n'
    "\t\treturn foreverServeCmd(ctx, args[1:], stdout, stderr)\n"
    '\tcase "rpc":\n'
    "\t\treturn foreverRPCCmd(args[1:], stdout, stderr)\n"
    "\tdefault:\n"
    "\t\treturn exitUsage\n"
    "\t}\n"
    "}\n"
)


def page(title="A page", description="What the page lets the reader do.", body="# What this is\n\nText.\n"):
    return (
        "---\n"
        "type: Guide\n"
        "title: %s\n"
        "description: %s\n"
        "tags: [test, page]\n"
        "status: stable\n"
        "generated: { by: omp/deepseek-v4.1-flash, at: %s }\n"
        "sources:\n"
        "  - id: sample\n"
        "    resource: cmd/forever-agent/forever_serve.go\n"
        "  - id: other\n"
        "    resource: internal/rpc/server.go\n"
        "---\n"
        "\n%s" % (title, description, GOOD_AT, body)
    )


ROOT_INDEX = (
    "---\n"
    "okf_version: 0.2\n"
    "---\n"
    "\n"
    "# Start here\n"
    "\n"
    "* [Getting started](getting-started/) - The first pages a new reader opens.\n"
    "* [Daemon](daemon/) - Driving a session over JSON-RPC.\n"
    "\n"
    "# Log\n"
    "\n"
    "* [Update log](log.md) - When each part of the manual changed.\n"
)

AREA_INDEX = (
    "# Getting started\n"
    "\n"
    "* [A page](page.md) - What the page lets the reader do.\n"
)

LOG = (
    "# Directory Update Log\n"
    "\n"
    "## 2026-11-24\n"
    "* **Creation**: Established the [bundle](index.md) and the [first page](getting-started/page.md).\n"
    "\n"
    "## 2026-11-20\n"
    "* **Initialization**: Created the directory.\n"
)


def write(root, path, text):
    full = os.path.join(root, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8") as handle:
        handle.write(text)


def good_bundle(root):
    write(root, "docs/manual/index.md", ROOT_INDEX)
    write(root, "docs/manual/log.md", LOG)
    write(root, "docs/manual/getting-started/index.md", AREA_INDEX)
    page_text = page(body="# What this is\n\nSee the [daemon page](../daemon/serve.md) and [the log](../log.md).\n")
    write(root, "docs/manual/getting-started/page.md", page_text)
    write(root, "docs/manual/daemon/index.md", "# Daemon\n\n* [forever serve](serve.md) - The session daemon.\n")
    write(root, "docs/manual/daemon/serve.md", page(title="forever serve", description="Run the session daemon."))
    write(root, "cmd/forever-agent/main.go", MAIN_GO)
    write(root, "cmd/forever-agent/forever_login.go", FOREVER_GO)


class BundleTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = self._tmp.name
        good_bundle(self.root)

    def tearDown(self):
        self._tmp.cleanup()

    def problems(self):
        return verify_okf.check_bundle(self.root)

    def assertProblem(self, needle):
        found = self.problems()
        self.assertTrue(
            any(needle in problem for problem in found),
            "no problem mentions %r; got:\n%s" % (needle, "\n".join(found) or "(none)"),
        )

    # The bundle every other test is built from.
    def test_good_bundle_passes(self):
        self.assertEqual(self.problems(), [])

    def test_missing_frontmatter(self):
        write(self.root, "docs/manual/getting-started/page.md", "# A page\n\nNo frontmatter.\n")
        self.assertProblem("frontmatter does not parse")

    def test_unparseable_frontmatter(self):
        write(self.root, "docs/manual/getting-started/page.md", page().replace("status: stable\n", "status stable\n"))
        self.assertProblem("frontmatter does not parse")

    def test_unknown_type(self):
        write(self.root, "docs/manual/getting-started/page.md", page().replace("type: Guide", "type: Novel"))
        self.assertProblem("unknown type 'Novel'")

    def test_missing_title(self):
        write(self.root, "docs/manual/getting-started/page.md", page().replace("title: A page\n", ""))
        self.assertProblem("no title")

    def test_missing_description(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace("description: What the page lets the reader do.\n", ""),
        )
        self.assertProblem("no description")

    def test_generated_at_not_utc(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace(GOOD_AT, "2026-11-24T20:35:00+02:00"),
        )
        self.assertProblem("not an ISO 8601 UTC timestamp")

    def test_generated_at_impossible_date(self):
        write(self.root, "docs/manual/getting-started/page.md", page().replace(GOOD_AT, "2026-13-40T25:61:00Z"))
        self.assertProblem("not a real date and time")

    def test_missing_generated(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace("generated: { by: omp/deepseek-v4.1-flash, at: %s }\n" % GOOD_AT, ""),
        )
        self.assertProblem("no generated")

    def test_unknown_status(self):
        write(self.root, "docs/manual/getting-started/page.md", page().replace("status: stable", "status: final"))
        self.assertProblem("unknown status 'final'")

    def test_area_index_with_frontmatter(self):
        write(
            self.root,
            "docs/manual/getting-started/index.md",
            "---\nokf_version: 0.2\n---\n" + AREA_INDEX,
        )
        self.assertProblem("index.md must not carry frontmatter")

    def test_root_index_with_other_frontmatter(self):
        write(self.root, "docs/manual/index.md", ROOT_INDEX.replace("okf_version: 0.2", "okf_version: 0.2\ntype: Guide"))
        self.assertProblem("may carry only okf_version")

    def test_index_missing_page(self):
        write(self.root, "docs/manual/getting-started/index.md", "# Getting started\n")
        self.assertProblem("index does not list page.md")

    def test_index_duplicate_entry(self):
        write(
            self.root,
            "docs/manual/getting-started/index.md",
            AREA_INDEX + "* [Again](page.md) - The same page twice.\n",
        )
        self.assertProblem("more than once")

    def test_index_broken_link(self):
        write(
            self.root,
            "docs/manual/getting-started/index.md",
            AREA_INDEX + "\n# Gone\n\n* [Gone](gone.md) - A page that is not there.\n",
        )
        self.assertProblem("link does not resolve: gone.md")

    def test_log_dates_out_of_order(self):
        write(
            self.root,
            "docs/manual/log.md",
            "# Directory Update Log\n\n## 2026-11-20\n* **Creation**: First.\n\n## 2026-11-24\n* **Update**: Second.\n",
        )
        self.assertProblem("newest first")

    def test_log_bad_heading(self):
        write(self.root, "docs/manual/log.md", "# Directory Update Log\n\n## 24 November 2026\n* **Creation**: First.\n")
        self.assertProblem("not ## YYYY-MM-DD")

    def test_log_with_frontmatter(self):
        write(self.root, "docs/manual/log.md", "---\ntype: Guide\n---\n" + LOG)
        self.assertProblem("log.md must not carry frontmatter")

    def test_broken_link_between_pages(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nSee [nowhere](../daemon/gone.md).\n"))
        self.assertProblem("link does not resolve: ../daemon/gone.md")

    def test_link_out_of_bundle_must_exist(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nSee [CI](../../CI.md).\n"))
        self.assertProblem("link does not resolve: ../../CI.md")
        write(self.root, "docs/CI.md", "# CI\n")
        self.assertEqual(self.problems(), [])

    def test_external_links_are_skipped(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nSee [the site](https://example.invalid/doc) and [a heading](#what).\n"),
        )
        self.assertEqual(self.problems(), [])

    def test_privacy_ipv4(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nConnect to 203.0.113.9.\n"))
        self.assertProblem("IPv4 address in the manual: 203.0.113.9")

    def test_privacy_loopback_is_allowed(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nListen on 127.0.0.1:8765.\n"))
        self.assertEqual(self.problems(), [])

    def test_privacy_home_path(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nWrite to %s.\n" % HOME_PATH))
        self.assertProblem("absolute home directory path")

    def test_privacy_windows_path(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nSave it in %s.\n" % WINDOWS_PATH))
        self.assertProblem("Windows drive path")

    def test_privacy_password_value(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nRun it with %s.\n" % PASSWORD_VALUE))
        self.assertProblem("secret value in the manual")

    def test_privacy_quoted_and_nested_secret_values(self):
        forms = (
            'password="%s"' % LITERAL_VALUE,
            "password='%s'" % LITERAL_VALUE,
            '"password": "%s"' % LITERAL_VALUE,
            "password: %s" % LITERAL_VALUE,
            "export DB_PASSWORD=%s" % LITERAL_VALUE,
            "--password=%s" % LITERAL_VALUE,
            "token: abc123",
        )
        for form in forms:
            with self.subTest(form=form):
                write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\nRun it with %s.\n" % form))
                self.assertProblem("secret value in the manual")

    def test_privacy_password_placeholder_is_allowed(self):
        body = (
            "# What\n\nRun it with --password=<password>, password=\"$PASSWORD\",\n"
            "\"password\": \"<password>\", password_file: \"./password.txt\",\n"
            "export FOREVER_PASSWORD='<password>', GH_TOKEN=...\n"
            "and a prose line, no password: --pass-file or FOREVER_PASSWORD.\n"
        )
        write(self.root, "docs/manual/getting-started/page.md", page(body=body))
        self.assertEqual(self.problems(), [])

    def test_directory_without_index(self):
        os.remove(os.path.join(self.root, "docs", "manual", "getting-started", "index.md"))
        self.assertProblem("directory has no index.md")

    def test_non_markdown_file_is_an_error(self):
        write(self.root, "docs/manual/getting-started/diagram.png", "not really a picture\n")
        self.assertProblem("not a Markdown file")

    def test_privacy_in_non_markdown_file(self):
        write(self.root, "docs/manual/notes.txt", "Connect to 203.0.113.9.\n")
        self.assertProblem("not a Markdown file")
        self.assertProblem("IPv4 address in the manual: 203.0.113.9")

    def test_unknown_command_in_fence(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n```sh\nforever-agent nosuch --x\n```\n"),
        )
        self.assertProblem("unknown forever-agent command: nosuch")

    def test_unknown_forever_subcommand_in_fence(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n```sh\nforever-agent forever nosuch --x\n```\n"),
        )
        self.assertProblem("unknown forever subcommand: nosuch")

    def test_registered_commands_pass(self):
        body = (
            "# What\n\n```sh\nforever-agent version\n"
            "forever-agent forever serve --dir ./slot\n"
            "bin/forever-agent lease acquire example --slot 1 --holder work\n```\n"
        )
        write(self.root, "docs/manual/getting-started/page.md", page(body=body))
        self.assertEqual(self.problems(), [])

    def test_version_output_is_not_a_command(self):
        write(self.root, "docs/manual/getting-started/page.md", page(body="# What\n\n```text\nforever-agent 0.1.0\n```\n"))
        self.assertEqual(self.problems(), [])

    def test_command_outside_a_fence_is_ignored(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nRun forever-agent nosuch when you like.\n"),
        )
        self.assertEqual(self.problems(), [])


class CommandLineTest(unittest.TestCase):
    def run_checker(self, root):
        return subprocess.run(
            [sys.executable, os.path.join(HERE, "verify_okf.py"), "--root", root],
            capture_output=True,
            text=True,
        )

    def test_exit_codes_and_one_line_per_problem(self):
        with tempfile.TemporaryDirectory() as root:
            good_bundle(root)
            clean = self.run_checker(root)
            self.assertEqual(clean.returncode, 0, clean.stderr)
            self.assertIn("verify_okf: ok", clean.stdout)

            write(root, "docs/manual/getting-started/page.md", page().replace("type: Guide", "type: Novel"))
            broken = self.run_checker(root)
            self.assertEqual(broken.returncode, 1)
            lines = [line for line in broken.stdout.splitlines() if line]
            self.assertEqual(len(lines), 1, broken.stdout)
            self.assertIn("unknown type 'Novel'", lines[0])


if __name__ == "__main__":
    unittest.main()
