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

# The three registration shapes the checker parses: literal names, a map literal a range walks, and the
# refusedMethods slice.
SERVE_METHODS_GO = (
    "package world\n"
    "\n"
    "func registerMethods(srv *rpc.Server, st *serveState) {\n"
    '\tsrv.Register("status", nil)\n'
    '\tsrv.Register("walk_to", nil)\n'
    "\tregisterMailMethods(srv, st)\n"
    "}\n"
    "\n"
    "func registerMailMethods(srv *rpc.Server, st *serveState) {\n"
    "\tfor name, fn := range map[string]func(*session) error{\n"
    '\t\t"mail_list": (*session).serveMailList,\n'
    "\t} {\n"
    "\t\tfn := fn\n"
    "\t\tsrv.Register(name, fn)\n"
    "\t}\n"
    "}\n"
    "\n"
    "var refusedMethods = []serveRefusal{\n"
    '\t{name: "dungeon_run", why: "the old driver"},\n'
    '\t{name: "tune_set", why: "the brain is retired"},\n'
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
        "generated: { by: %s, at: %s }\n"
        "sources:\n"
        "  - id: sample\n"
        "    resource: cmd/forever-agent/main.go\n"
        "  - id: other\n"
        "    resource: internal/forever/world/serve_methods.go\n"
        "---\n"
        "\n%s" % (title, description, verify_okf.GENERATED_BY, GOOD_AT, body)
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


METHOD_INDEX = page(
    title="Method index",
    description="Every method the daemon registers.",
    body="# Methods\n\n| Method | Params |\n|---|---|\n| `status` | `{}` |\n| `walk_to` | `x`, `y`, `z` |\n| `mail_list` | `guid` |\n",
)

NOT_PORTED = page(
    title="Methods not ported",
    description="The names that answer with a refusal.",
    body="# Names\n\n| Methods | Area |\n|---|---|\n| `dungeon_run`, `tune_set` | The old driver |\n",
)


def good_bundle(root):
    write(root, "docs/manual/index.md", ROOT_INDEX)
    write(root, "docs/manual/log.md", LOG)
    write(root, "docs/manual/getting-started/index.md", AREA_INDEX)
    write(
        root,
        "docs/manual/getting-started/page.md",
        page(body="# What this is\n\nSee the [daemon page](../daemon/serve.md) and [the log](../log.md).\n"),
    )
    write(
        root,
        "docs/manual/daemon/index.md",
        "# Daemon\n\n* [forever serve](serve.md) - Run the session daemon.\n"
        "* [Method index](method-index.md) - Every method the daemon registers.\n"
        "* [Methods not ported](not-ported-methods.md) - The names that answer with a refusal.\n",
    )
    serve_body = (
        "# What this is\n\n```sh\nforever-agent version\nforever-agent lease acquire example --slot 1 --holder work\n"
        "forever-agent forever serve --dir ./slot\nforever-agent forever rpc ./slot status\n```\n"
    )
    write(
        root,
        "docs/manual/daemon/serve.md",
        page(title="forever serve", description="Run the session daemon.", body=serve_body),
    )
    write(root, "docs/manual/daemon/method-index.md", METHOD_INDEX)
    write(root, "docs/manual/daemon/not-ported-methods.md", NOT_PORTED)
    write(root, "cmd/forever-agent/main.go", MAIN_GO)
    write(root, "cmd/forever-agent/forever_login.go", FOREVER_GO)
    write(root, "internal/forever/world/serve_methods.go", SERVE_METHODS_GO)


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
            page().replace("generated: { by: %s, at: %s }\n" % (verify_okf.GENERATED_BY, GOOD_AT), ""),
        )
        self.assertProblem("no generated")

    # The failing case the rule pins: a page that names its writer instead of the project.
    def test_generated_by_must_name_the_project(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace(verify_okf.GENERATED_BY, "maintainer/1.0"),
        )
        self.assertProblem("must be 'forever-agent docs'")

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

    # Command coverage: every registered command is mentioned somewhere, and the allow-list cannot rot.

    def test_registered_command_must_be_mentioned(self):
        write(
            self.root,
            "cmd/forever-agent/main.go",
            MAIN_GO.replace('\tcase "lease":', '\tcase "unlisted":\n\t\treturn exitUsage\n\tcase "lease":'),
        )
        self.assertProblem("registered command 'unlisted' is in no manual page")

    def test_command_allow_list_allows_a_missing_command(self):
        write(
            self.root,
            "cmd/forever-agent/main.go",
            MAIN_GO.replace('\tcase "lease":', '\tcase "unlisted":\n\t\treturn exitUsage\n\tcase "lease":'),
        )
        write(self.root, "docs/manual/.undocumented-commands", "# hidden commands\nunlisted # a debug-only command\n")
        self.assertEqual(self.problems(), [])

    def test_command_allow_list_entry_must_be_registered(self):
        write(self.root, "docs/manual/.undocumented-commands", "nosuch # a command that is gone\n")
        self.assertProblem("allow-list entry 'nosuch' is not a registered command")

    def test_command_allow_list_entry_needs_a_reason(self):
        write(self.root, "docs/manual/.undocumented-commands", "version\n")
        self.assertProblem("allow-list entry 'version' has no reason")

    def test_forever_subcommand_needs_the_forever_form(self):
        write(
            self.root,
            "cmd/forever-agent/forever_login.go",
            FOREVER_GO.replace('\tcase "rpc":', '\tcase "zzz":\n\t\treturn exitUsage\n\tcase "rpc":'),
        )
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n`zzz` alone is not the form.\n\n```sh\nforever zzz\n```\n"),
        )
        self.assertProblem("registered forever subcommand 'zzz'")
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nRun it:\n\n```sh\nforever-agent forever zzz\n```\n"),
        )
        self.assertEqual(self.problems(), [])

    # Method coverage: the method index and the not-ported page against the serve's registration.

    def test_parse_method_registry_reads_all_three_shapes(self):
        implemented, refused = verify_okf.parse_method_registry([SERVE_METHODS_GO])
        self.assertEqual(implemented, {"status", "walk_to", "mail_list"})
        self.assertEqual(refused, {"dungeon_run", "tune_set"})

    def test_a_register_name_through_a_variable_is_an_error(self):
        text = (
            "package world\n"
            "\n"
            "func register(srv *rpc.Server) {\n"
            '\tconst nm = "status"\n'
            "\tsrv.Register(nm, nil)\n"
            "}\n"
        )
        problems = []
        implemented, refused = verify_okf.parse_method_registry([text], problems)
        self.assertEqual(implemented, set())
        self.assertEqual(refused, set())
        self.assertEqual(len(problems), 1)
        self.assertIn("Use Register", problems[0][2])

    def test_a_register_call_split_over_lines_is_read(self):
        text = (
            "package world\n"
            "\n"
            "func register(srv *rpc.Server) {\n"
            "\tsrv.Register(\n"
            '\t\t"split_name",\n'
            "\t\tnil,\n"
            "\t)\n"
            "}\n"
        )
        implemented, refused = verify_okf.parse_method_registry([text])
        self.assertEqual(implemented, {"split_name"})

    def test_an_aliased_server_variable_is_read(self):
        text = (
            "package world\n"
            "\n"
            "func register(srv *rpc.Server) {\n"
            "\ts2 := srv\n"
            '\ts2.Register("aliased_name", nil)\n'
            "}\n"
        )
        implemented, refused = verify_okf.parse_method_registry([text])
        self.assertEqual(implemented, {"aliased_name"})

    def test_a_range_over_a_string_slice_is_read(self):
        text = (
            "package world\n"
            "\n"
            "func register(srv *rpc.Server) {\n"
            '\tfor _, name := range []string{"one", "two"} {\n'
            "\t\tsrv.Register(name, nil)\n"
            "\t}\n"
            "}\n"
        )
        implemented, refused = verify_okf.parse_method_registry([text])
        self.assertEqual(implemented, {"one", "two"})

    def test_a_range_over_an_unknown_slice_fails_closed(self):
        text = (
            "package world\n"
            "\n"
            "func register(srv *rpc.Server, names []string) {\n"
            "\tfor _, name := range names {\n"
            "\t\tsrv.Register(name, nil)\n"
            "\t}\n"
            "}\n"
        )
        problems = []
        verify_okf.parse_method_registry([text], problems)
        self.assertEqual(len(problems), 1)
        self.assertIn("Use Register", problems[0][2])

    def test_the_refused_list_registration_is_refused_not_implemented(self):
        text = (
            "package world\n"
            "\n"
            "var refusedMethods = []serveRefusal{\n"
            '\t{name: "old_one", why: "gone"},\n'
            "}\n"
            "\n"
            "func registerRefused(srv *rpc.Server) {\n"
            "\tfor _, r := range refusedMethods {\n"
            "\t\tname := r.name\n"
            '\t\tsrv.Register(name, func() {})\n'
            "\t}\n"
            "}\n"
        )
        implemented, refused = verify_okf.parse_method_registry([text])
        self.assertEqual(implemented, set())
        self.assertEqual(refused, {"old_one"})

    def test_registered_method_must_be_in_the_index(self):
        write(
            self.root,
            "internal/forever/world/serve_methods.go",
            SERVE_METHODS_GO.replace(
                'srv.Register("status", nil)',
                'srv.Register("status", nil)\n\tsrv.Register("undocumented_method", nil)',
            ),
        )
        self.assertProblem("registered daemon method 'undocumented_method' has no row")

    def test_method_allow_list_allows_a_missing_method(self):
        write(
            self.root,
            "internal/forever/world/serve_methods.go",
            SERVE_METHODS_GO.replace(
                'srv.Register("status", nil)',
                'srv.Register("status", nil)\n\tsrv.Register("bench_only", nil)',
            ),
        )
        write(self.root, "docs/manual/.undocumented-methods", "bench_only # a bench-only method\n")
        self.assertEqual(self.problems(), [])

    def test_method_allow_list_entry_must_be_registered(self):
        write(self.root, "docs/manual/.undocumented-methods", "nosuch # a method that is gone\n")
        self.assertProblem("allow-list entry 'nosuch' is not a registered daemon method")

    def test_index_method_must_be_registered(self):
        write(
            self.root,
            "docs/manual/daemon/method-index.md",
            page(
                title="Method index",
                description="Every method the daemon registers.",
                body="# Methods\n\n| Method | Params |\n|---|---|\n| `status` | `{}` |\n"
                "| `walk_to` | `x` |\n| `mail_list` | `guid` |\n| `ghost` | none |\n",
            ),
        )
        self.assertProblem("method 'ghost' is not registered by the serve")

    def test_not_ported_method_must_be_on_its_page(self):
        write(
            self.root,
            "internal/forever/world/serve_methods.go",
            SERVE_METHODS_GO.replace(
                '\t{name: "tune_set", why: "the brain is retired"},\n',
                '\t{name: "tune_set", why: "the brain is retired"},\n'
                '\t{name: "lfg_status", why: "no client finder"},\n',
            ),
        )
        self.assertProblem("refused method 'lfg_status' is named in no row")

    # The parser bypasses the review demonstrated: several labels on one case, a nested switch, and a
    # brace inside a string or a raw string.

    def test_case_labels_can_name_several_commands(self):
        text = MAIN_GO.replace(
            '\tcase "lease":',
            '\tcase "one", "two":\n\t\treturn exitUsage\n\tcase "lease":',
        )
        top, _ = verify_okf.parse_command_registry(text, FOREVER_GO)
        self.assertIn("one", top)
        self.assertIn("two", top)

    def test_a_nested_switch_case_is_not_a_command(self):
        text = MAIN_GO.replace(
            '\tcase "lease":\n\t\treturn leaseCmd(args[1:], stdout, stderr)',
            '\tcase "lease":\n'
            "\t\tswitch x {\n"
            '\t\tcase "nested":\n'
            "\t\t}\n"
            "\t\treturn leaseCmd(args[1:], stdout, stderr)",
        )
        top, _ = verify_okf.parse_command_registry(text, FOREVER_GO)
        self.assertIn("lease", top)
        self.assertNotIn("nested", top)

    def test_braces_in_strings_do_not_end_the_switch(self):
        text = MAIN_GO.replace(
            '\tcase "lease":',
            '\tcase "brace":\n'
            '\t\t_ = "}"\n'
            '\t\t_ = `case "ghost":`\n'
            '\tcase "lease":',
        )
        top, _ = verify_okf.parse_command_registry(text, FOREVER_GO)
        self.assertIn("brace", top)
        self.assertIn("lease", top)
        self.assertNotIn("ghost", top)

    def test_a_bare_word_in_a_plain_table_names_no_command(self):
        top, forever = verify_okf.command_mentions("| Flag | Meaning |\n|---|---|\n| `version` | x |\n")
        self.assertEqual(top, set())
        self.assertEqual(forever, set())

    def test_a_bare_word_under_a_command_header_names_a_command(self):
        top, _ = verify_okf.command_mentions("| Command | What |\n|---|---|\n| `version` | x |\n")
        self.assertEqual(top, {"version"})

    def test_a_named_command_header_names_a_command(self):
        top, _ = verify_okf.command_mentions(
            "| Turtle-only command | Replacement |\n|---|---|\n| `version` | x |\n"
        )
        self.assertEqual(top, {"version"})

    def test_a_command_form_in_a_plain_table_names_a_command(self):
        top, forever = verify_okf.command_mentions(
            "| Flag | Meaning |\n|---|---|\n| `--x` | `forever-agent version` |\n"
        )
        self.assertEqual(top, {"version"})
        self.assertEqual(forever, set())

    def test_a_bare_word_in_prose_names_no_command(self):
        top, _ = verify_okf.command_mentions("# What\n\nCall `version` when you like.\n")
        self.assertEqual(top, set())

    def test_a_tilde_fence_is_scanned_for_commands(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n~~~sh\nforever-agent nosuch --x\n~~~\n"),
        )
        self.assertProblem("unknown forever-agent command: nosuch")

    def test_an_indented_code_block_is_scanned_for_commands(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n    forever-agent nosuch --x\n"),
        )
        self.assertProblem("unknown forever-agent command: nosuch")

    def test_a_crlf_page_is_a_problem(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace("\n", "\r\n"),
        )
        self.assertProblem("CRLF line endings")

    def test_a_source_resource_must_exist(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace("resource: internal/forever/world/serve_methods.go", "resource: internal/gone/gone.go"),
        )
        self.assertProblem("source resource does not exist")

    def test_a_source_url_is_not_a_path(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page().replace("resource: internal/forever/world/serve_methods.go", "resource: https://example.invalid/doc"),
        )
        self.assertEqual(self.problems(), [])

    def test_a_missing_anchor_is_a_problem(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nSee [nowhere](../daemon/serve.md#no-such-heading).\n"),
        )
        self.assertProblem("link anchor does not resolve")

    def test_an_existing_anchor_passes(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nSee [there](../daemon/serve.md#what-this-is).\n"),
        )
        self.assertEqual(self.problems(), [])

    def test_an_index_description_must_match_the_page(self):
        write(
            self.root,
            "docs/manual/getting-started/index.md",
            "# Getting started\n\n* [A page](page.md) - A different summary.\n",
        )
        self.assertProblem("index description does not match")

    # Commands are documented as commands, not as a word in backticks.

    def test_a_bare_backticked_command_is_not_documentation(self):
        write(
            self.root,
            "cmd/forever-agent/main.go",
            MAIN_GO.replace('\tcase "lease":', '\tcase "unlisted":\n\t\treturn exitUsage\n\tcase "lease":'),
        )
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n`unlisted` is mentioned in prose only.\n"),
        )
        self.assertProblem("registered command 'unlisted' is in no manual page")

    def test_a_heading_documents_a_command(self):
        write(
            self.root,
            "cmd/forever-agent/main.go",
            MAIN_GO.replace('\tcase "lease":', '\tcase "unlisted":\n\t\treturn exitUsage\n\tcase "lease":'),
        )
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n## unlisted\n\nWhat the command does.\n"),
        )
        self.assertEqual(self.problems(), [])

    def test_a_table_row_documents_a_command(self):
        write(
            self.root,
            "cmd/forever-agent/main.go",
            MAIN_GO.replace('\tcase "lease":', '\tcase "unlisted":\n\t\treturn exitUsage\n\tcase "lease":'),
        )
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\n| Command | What it does |\n|---|---|\n| `forever-agent unlisted` | A check. |\n"),
        )
        self.assertEqual(self.problems(), [])

    # The privacy rule: e-mail addresses, all the home path forms, and real host names.

    def test_privacy_email_address(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nWrite to %s.\n" % ("alice" + "@" + "real" + ".org")),
        )
        self.assertProblem("e-mail address in the manual")

    def test_privacy_placeholder_email_is_allowed(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nWrite to %s.\n" % ("account" + "@" + "example.com")),
        )
        self.assertEqual(self.problems(), [])

    def test_privacy_root_path(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nSave it in %s.\n" % ("/ro" + "ot/secret")),
        )
        self.assertProblem("absolute home directory path")

    def test_privacy_users_path(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nSave it in %s.\n" % ("/Us" + "ers/alice/secret")),
        )
        self.assertProblem("absolute home directory path")

    def test_privacy_host_name(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nConnect to %s.\n" % ("db" + ".real" + ".org")),
        )
        self.assertProblem("host name in the manual: db.real.org")

    def test_privacy_allowed_hosts(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(
                body="# What\n\nSee https://github.com/x, %s and %s.\n"
                % ("trinity.actual" + ".battle" + ".net", "login" + ".example.invalid")
            ),
        )
        self.assertEqual(self.problems(), [])

    def test_privacy_private_tld_host(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nConnect to %s.\n" % ("build01.corp." + "internal")),
        )
        self.assertProblem("host name in the manual: build01.corp.internal")

    def test_privacy_machine_name_with_an_unknown_tld(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nConnect to %s.\n" % ("build01." + "corp")),
        )
        self.assertProblem("host name in the manual: build01.corp")

    def test_privacy_ipv6_address(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nListen on %s.\n" % ("fe80" + "::1")),
        )
        self.assertProblem("IPv6 address in the manual")

    def test_privacy_ipv6_loopback_is_allowed(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(body="# What\n\nListen on %s.\n" % ("::" + "1")),
        )
        self.assertEqual(self.problems(), [])

    def test_privacy_file_names_and_symbols_are_not_hosts(self):
        write(
            self.root,
            "docs/manual/getting-started/page.md",
            page(
                body="# What\n\nRead serve_rpc_methods.go and context.Context; the file is named "
                "rpc-8765.token.\n"
            ),
        )
        self.assertEqual(self.problems(), [])

    def test_not_ported_page_method_must_still_be_unimplemented(self):
        write(
            self.root,
            "docs/manual/daemon/not-ported-methods.md",
            page(
                title="Methods not ported",
                description="The names that answer with a refusal.",
                body="# Names\n\n| Methods | Area |\n|---|---|\n"
                "| `dungeon_run`, `tune_set`, `status` | The old driver |\n",
            ),
        )
        self.assertProblem("method 'status' is not in refusedMethods any more")


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
