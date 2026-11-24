#!/usr/bin/env python3
"""Invariants of the two labelling workflows (.github/workflows/label-issues.yml and label-pr.yml).

Run from the module root (`make tools-test` runs this module too):

  python3 -m unittest tools/test_workflows.py

The files are read as text, with no YAML library: the tests run on a bare python3. What they lock in is the part of the
workflows a later edit could quietly weaken: the issue trigger, the SHA pins, the absence of a checkout in the
pull-request workflow, the minimal permissions, and no event expression inside a `run:` script.
"""
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = ROOT / ".github" / "workflows"
ISSUES_FILE = WORKFLOWS / "label-issues.yml"
PR_FILE = WORKFLOWS / "label-pr.yml"

# A pinned action: name@<40 hex>. A tag (`@v4`) or a short SHA would not survive an upstream tag move.
SHA_PIN = re.compile(r"^[^@\s]+@[0-9a-f]{40}$")
USES = re.compile(r"^\s*(?:-\s+)?uses:\s*(\S+)")
# A step may write `run:` on its own line or as `- run:` on the step's first line.
RUN = re.compile(r"^(?:-\s+)?run:(.*)$")


def _uncommented(line):
    """The line without its comment: `#` starts one at the line start or after whitespace (a `#` inside a value stays)."""
    if line.lstrip().startswith("#"):
        return ""
    match = re.search(r"\s#", line)
    return line[:match.start()] if match else line


def _indent(line):
    return len(line) - len(line.lstrip(" "))


def _block(lines, key, indent):
    """The lines under a `<key>:` entry at `indent`: the following lines that are blank or indented deeper."""
    for i, line in enumerate(lines):
        if _indent(_uncommented(line)) == indent and _uncommented(line).strip().startswith(key + ":"):
            block = []
            for line in lines[i + 1:]:
                if line.strip() and _indent(line) <= indent:
                    break
                block.append(line)
            return block
    raise AssertionError("no %r entry at indent %d" % (key, indent))


def _mapping(lines, indent):
    """The `key: value` pairs at `indent` of a block, in file order; a bare `key:` gives an empty string."""
    pairs = {}
    for line in lines:
        text = _uncommented(line).strip()
        if not text or text.startswith("- ") or _indent(_uncommented(line)) != indent:
            continue
        key, _, value = text.partition(":")
        pairs[key.strip()] = value.strip()
    return pairs


def _inline_list(value):
    """`[opened]` -> ["opened"]; anything that is not an inline list fails, so a shape change cannot pass silently."""
    if not (value.startswith("[") and value.endswith("]")):
        raise AssertionError("not an inline list: %r" % value)
    return [item.strip() for item in value[1:-1].split(",") if item.strip()]


def _uses(text):
    return [match.group(1) for line in text.splitlines() for match in [USES.match(_uncommented(line))] if match]


def _run_blocks(text):
    """Every `run:` script: its inline value and the deeper-indented lines after it, joined."""
    lines = text.splitlines()
    blocks = []
    i = 0
    while i < len(lines):
        line = _uncommented(lines[i])
        match = RUN.match(line.strip())
        if match:
            indent = _indent(line)
            block = [match.group(1)]
            i += 1
            while i < len(lines) and (not lines[i].strip() or _indent(lines[i]) > indent):
                block.append(lines[i])
                i += 1
            blocks.append("\n".join(block))
            continue
        i += 1
    return blocks


class LabelIssuesTest(unittest.TestCase):
    """label-issues.yml: one run, when an issue opens; pinned action; minimal permissions; text never in the shell."""

    def setUp(self):
        self.text = ISSUES_FILE.read_text(encoding="utf8")
        self.lines = self.text.splitlines()

    def test_trigger_is_issues_opened_only(self):
        triggers = _mapping(_block(self.lines, "on", 0), 2)
        self.assertEqual(["issues"], list(triggers))
        types = _mapping(_block(_block(self.lines, "on", 0), "issues", 2), 4)
        self.assertEqual(["opened"], _inline_list(types["types"]))

    def test_uses_are_pinned(self):
        uses = _uses(self.text)
        self.assertTrue(uses, "no `uses:` found: the parse or the file changed")
        for action in uses:
            self.assertRegex(action, SHA_PIN, "%s is not pinned to a 40-hex SHA" % action)

    def test_permissions_are_minimal(self):
        self.assertEqual(1, sum(1 for line in self.lines if _uncommented(line).strip().startswith("permissions:")))
        self.assertEqual({"contents": "read", "issues": "write"},
                         _mapping(_block(self.lines, "permissions", 0), 2))

    def test_no_event_expression_in_a_run_script(self):
        blocks = _run_blocks(self.text)
        self.assertTrue(blocks, "no `run:` found: the parse or the file changed")
        for block in blocks:
            self.assertNotIn("${{ github.event.", block)


class LabelPrTest(unittest.TestCase):
    """label-pr.yml: pull_request_target without a checkout of the pull request's code, pinned action, minimal
    permissions, no event expression in a run script."""

    def setUp(self):
        self.text = PR_FILE.read_text(encoding="utf8")
        self.lines = self.text.splitlines()

    def test_no_checkout_step(self):
        uses = _uses(self.text)
        self.assertTrue(uses, "no `uses:` found: the parse or the file changed")
        for action in uses:
            self.assertNotIn("checkout", action, "a checkout of the pull request's code is the risk this trigger avoids")
        self.assertNotIn("checkout", [line for line in self.lines if "uses:" in _uncommented(line)])

    def test_uses_are_pinned(self):
        for action in _uses(self.text):
            self.assertRegex(action, SHA_PIN, "%s is not pinned to a 40-hex SHA" % action)

    def test_permissions_are_minimal(self):
        self.assertEqual(1, sum(1 for line in self.lines if _uncommented(line).strip().startswith("permissions:")))
        self.assertEqual({"contents": "read", "pull-requests": "write"},
                         _mapping(_block(self.lines, "permissions", 0), 2))

    def test_no_event_expression_in_a_run_script(self):
        for block in _run_blocks(self.text):
            self.assertNotIn("${{ github.event.", block)


if __name__ == "__main__":
    unittest.main()
