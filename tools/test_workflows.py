#!/usr/bin/env python3
"""Invariants of the two labelling workflows (.github/workflows/label-issues.yml and label-pr.yml).

Run from the module root (`make tools-test` runs this module too):

  python3 -m unittest tools/test_workflows.py

The files are read through `parse_workflow` below: a strict parser for the YAML subset these two workflows use (block
mappings and lists, plain scalars, `|` literal blocks and `[a, b]` flow sequences of plain scalars). Python's stdlib
has no YAML parser and the tests run on a bare python3, so every other construct - a quoted scalar, a flow mapping, an
anchor, an alias, a tag, a folded block, a second document, a tab indent, a duplicate key, a line that is neither
`key: value` nor `- item` - raises WorkflowSyntaxError. A construct the parser does not model must fail the test, never
be skipped: a quoted `"uses"` key would otherwise hide an unpinned action from every assertion here.

What the tests lock in: the issue trigger, the SHA pins, the absence of a checkout in the pull-request workflow, the
minimal permissions, and no event expression inside a `run:` script. They walk the whole parsed document, so a `uses:`
at job level (a reusable workflow) and a step in any list are checked like any other.
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
# `${{ github.event... }}` in any spelling GitHub accepts: the `${{-` trim marker, no space after `{{`, spaces around
# the dot (`github . event`), bracket access (`github['event']`) and upper case. `github.event_name` (and the bracket
# form of it) is a string constant, not the payload, and does not match.
EVENT_EXPRESSION = re.compile(
    r"\$\{\{-?\s*github\s*(?:\.\s*event\b|\[\s*['\"]?event['\"]?\s*\])", re.IGNORECASE)

KEY = re.compile(r"^([A-Za-z_][A-Za-z0-9_.-]*):(?: (.*))?$")
ITEM = re.compile(r"^- (.*)$")
FLOW_ITEM = re.compile(r"^[A-Za-z0-9_.-]+$")
# A value that starts with one of these is YAML syntax this parser does not model (or a reserved indicator).
UNSUPPORTED_START = ('"', "'", "&", "*", "!", "{", "}", ">", "@", "`", "%", "?")
ANCHOR_ALIAS_TAG = re.compile(r"(?:^|\s)[&*!][A-Za-z0-9_-]")


class WorkflowSyntaxError(AssertionError):
    """A workflow text outside the strict subset this module parses; the test fails rather than skipping the line."""


def _fail(source, line, message):
    raise WorkflowSyntaxError("%s:%d: %s" % (source, line, message))


def _uncommented(raw):
    """The line without its comment: `#` starts one at the line start or after whitespace."""
    if raw.lstrip().startswith("#"):
        return ""
    match = re.search(r"\s#", raw)
    return raw[:match.start()] if match else raw


def _content(lines, index, source):
    """(indent, text) of a line that carries content, or None for a blank or comment-only line."""
    raw = lines[index]
    leading = raw[:len(raw) - len(raw.lstrip())]
    if "\t" in leading:
        _fail(source, index + 1, "a tab in the indentation")
    line = _uncommented(raw).rstrip()
    text = line.strip()
    if not text:
        return None
    if text in ("---", "..."):
        _fail(source, index + 1, "a document marker (only one document, with no explicit start)")
    return len(line) - len(line.lstrip(" ")), text


def _plain(value, source, line):
    """A plain scalar: no YAML indicator the parser does not model, no anchor/alias/tag, no `: ` (a nested mapping)."""
    if value[0] in UNSUPPORTED_START:
        _fail(source, line, "an unsupported scalar %r (quoted, flow mapping, anchor, alias, tag or block indicator)"
              % value)
    if ANCHOR_ALIAS_TAG.search(value):
        _fail(source, line, "an anchor, alias or tag in %r" % value)
    if ": " in value:
        _fail(source, line, "a plain scalar with `: ` in %r (a nested mapping)" % value)
    return value


def _flow_sequence(value, source, line):
    """`[a, b]` -> ["a", "b"]: plain scalars only; a nested list or mapping fails."""
    if not value.endswith("]"):
        _fail(source, line, "an unterminated flow sequence %r" % value)
    inner = value[1:-1].strip()
    if not inner:
        _fail(source, line, "an empty flow sequence")
    items = []
    for part in inner.split(","):
        part = part.strip()
        if not FLOW_ITEM.match(part):
            _fail(source, line, "a flow sequence item outside the subset: %r" % part)
        items.append(part)
    return items


def _literal(lines, index, indent, source):
    """The body of a `|` block from `index` on: lines indented exactly two deeper, blank lines kept, end blanks
    dropped. A `|-`/`|+` chomping indicator or a folded `>` block is outside the subset (the caller rejects it)."""
    body = []
    while index < len(lines):
        raw = lines[index]
        if not raw.strip():
            body.append("")
            index += 1
            continue
        content_indent = len(raw) - len(raw.lstrip(" "))
        if content_indent <= indent:
            break
        if content_indent != indent + 2:
            _fail(source, index + 1, "a block scalar line not indented by %d" % (indent + 2))
        body.append(raw[content_indent:])
        index += 1
    while body and not body[-1].strip():
        body.pop()
    return "\n".join(body), index


def _value(value, lines, index, indent, source, line):
    """The value written after `key: `; a block scalar or flow sequence consumes following lines."""
    if value == "|":
        return _literal(lines, index, indent, source)
    if value.startswith("|") or value.startswith(">"):
        _fail(source, line, "a block scalar indicator outside the subset: %r" % value)
    if value.startswith("["):
        return _flow_sequence(value, source, line), index
    return _plain(value, source, line), index


def _child(lines, index, indent, source, line):
    """The value of a key whose colon ends the line: a deeper mapping, a list, or the empty string."""
    probe = index
    while probe < len(lines) and _content(lines, probe, source) is None:
        probe += 1
    if probe == len(lines):
        return "", index
    child_indent, text = _content(lines, probe, source)
    if child_indent <= indent:
        return "", index
    if child_indent != indent + 2:
        _fail(source, probe + 1, "an indentation of %d, want %d" % (child_indent, indent + 2))
    if text.startswith("- "):
        return _sequence(lines, index, indent + 2, source)
    return _mapping(lines, index, indent + 2, source)


def _mapping(lines, index, indent, source):
    """The `key: value` entries at `indent`, in file order; stops at a dedent, a list item or the end."""
    out = {}
    while index < len(lines):
        content = _content(lines, index, source)
        if content is None:
            index += 1
            continue
        line_indent, text = content
        if line_indent < indent:
            break
        if line_indent > indent:
            _fail(source, index + 1, "an indentation of %d, want %d" % (line_indent, indent))
        if text.startswith("- "):
            break
        match = KEY.match(text)
        if not match:
            _fail(source, index + 1, "a line that is neither `key: value` nor `- item`: %r" % text)
        key, value = match.group(1), match.group(2)
        if key in out:
            _fail(source, index + 1, "a duplicate key %r" % key)
        index += 1
        if value:
            out[key], index = _value(value, lines, index, indent, source, index)
        else:
            out[key], index = _child(lines, index, indent, source, index)
    return out, index


def _sequence(lines, index, indent, source):
    """The `- item` entries at `indent`; an item is a scalar or a mapping whose keys sit two deeper."""
    out = []
    while index < len(lines):
        content = _content(lines, index, source)
        if content is None:
            index += 1
            continue
        line_indent, text = content
        if line_indent < indent:
            break
        if line_indent > indent:
            _fail(source, index + 1, "an indentation of %d, want %d" % (line_indent, indent))
        match = ITEM.match(text)
        if not match:
            break
        rest = match.group(1)
        index += 1
        key = KEY.match(rest)
        if not key:
            out.append(_plain(rest, source, index))
            continue
        item = {}
        name, value = key.group(1), key.group(2)
        # The item's own keys sit at indent + 2, so a block scalar or nested block under one starts at indent + 4.
        if value:
            item[name], index = _value(value, lines, index, indent + 2, source, index)
        else:
            item[name], index = _child(lines, index, indent + 2, source, index)
        more, index = _mapping(lines, index, indent + 2, source)
        for other, other_value in more.items():
            if other in item:
                _fail(source, index, "a duplicate key %r in one item" % other)
            item[other] = other_value
        out.append(item)
    return out, index


def parse_workflow(text, source="workflow"):
    """The workflow as nested dicts and lists, or WorkflowSyntaxError on any construct outside the subset."""
    lines = text.splitlines()
    doc, index = _mapping(lines, 0, 0, source)
    while index < len(lines):
        if _content(lines, index, source) is not None:
            _fail(source, index + 1, "content after the top-level mapping")
        index += 1
    if not doc:
        _fail(source, 1, "an empty workflow")
    return doc


def _walk(node):
    """Every node of the tree, the mappings and lists included."""
    yield node
    if isinstance(node, dict):
        children = node.values()
    elif isinstance(node, list):
        children = node
    else:
        return
    for child in children:
        yield from _walk(child)


def values_for(doc, key):
    """Every value stored under `key` anywhere in the document, job level and step level alike."""
    found = []
    for node in _walk(doc):
        if isinstance(node, dict):
            for name, value in node.items():
                if name == key:
                    found.append(value)
    return found


def assert_pinned_actions(doc):
    """Every `uses:` is a full-SHA pin; there is at least one, so an empty scan cannot pass by finding nothing."""
    uses = values_for(doc, "uses")
    if not uses:
        raise AssertionError("no `uses:` found: the parse or the file changed")
    for action in uses:
        if not isinstance(action, str):
            raise AssertionError("a `uses:` that is not a scalar: %r" % (action,))
        if not SHA_PIN.match(action):
            raise AssertionError("%s is not pinned to a 40-hex SHA" % action)
    return uses


def assert_no_checkout(doc):
    """No action in this file is a checkout: a checkout of the pull request's code is the risk label-pr avoids.

    GitHub resolves an owner and repository name case-insensitively, so `Actions/Checkout@<sha>` is the same action as
    `actions/checkout@<sha>`: the reference is compared before its `@` and casefolded.
    """
    uses = assert_pinned_actions(doc)
    for action in uses:
        if "checkout" in action.split("@", 1)[0].casefold():
            raise AssertionError("a checkout step: %s" % action)


def assert_permissions(doc, expected):
    """Exactly one permissions block, and it is `expected`: the minimal pair the job needs."""
    blocks = values_for(doc, "permissions")
    if len(blocks) != 1:
        raise AssertionError("expected one permissions block, found %d: %r" % (len(blocks), blocks))
    if blocks[0] != expected:
        raise AssertionError("permissions are %r, want %r" % (blocks[0], expected))


def assert_no_event_expression(doc, require_run=False):
    """No `run:` script interpolates an event payload: the title and body reach the shell only as environment."""
    scripts = values_for(doc, "run")
    if require_run and not scripts:
        raise AssertionError("no `run:` script found: the parse or the file changed")
    for script in scripts:
        if not isinstance(script, str):
            raise AssertionError("a `run:` that is not a scalar: %r" % (script,))
        match = EVENT_EXPRESSION.search(script)
        if match:
            raise AssertionError("a run script interpolates %r" % match.group(0))
    return scripts


class LabelIssuesTest(unittest.TestCase):
    """label-issues.yml: one run, when an issue opens; pinned action; minimal permissions; text never in the shell."""

    def setUp(self):
        self.text = ISSUES_FILE.read_text(encoding="utf8")
        self.doc = parse_workflow(self.text, str(ISSUES_FILE))

    def test_the_file_parses_inside_the_subset(self):
        self.assertEqual("label-issues", self.doc["name"])

    def test_trigger_is_issues_opened_only(self):
        self.assertEqual({"issues": {"types": ["opened"]}}, self.doc.get("on"))

    def test_uses_are_pinned(self):
        self.assertIn("actions/checkout@" + "11d5960a326750d5838078e36cf38b85af677262",
                      assert_pinned_actions(self.doc))

    def test_permissions_are_minimal(self):
        assert_permissions(self.doc, {"contents": "read", "issues": "write"})

    def test_no_event_expression_in_a_run_script(self):
        scripts = assert_no_event_expression(self.doc, require_run=True)
        self.assertEqual(["python3 -I tools/label_issue.py"], scripts)


class LabelPrTest(unittest.TestCase):
    """label-pr.yml: pull_request_target without a checkout of the pull request's code, pinned action, minimal
    permissions, no event expression in a run script."""

    def setUp(self):
        self.text = PR_FILE.read_text(encoding="utf8")
        self.doc = parse_workflow(self.text, str(PR_FILE))

    def test_no_checkout_step(self):
        assert_no_checkout(self.doc)

    def test_uses_are_pinned(self):
        assert_pinned_actions(self.doc)

    def test_permissions_are_minimal(self):
        assert_permissions(self.doc, {"contents": "read", "pull-requests": "write"})

    def test_no_event_expression_in_a_run_script(self):
        assert_no_event_expression(self.doc)


SHA = "11d5960a326750d5838078e36cf38b85af677262"


def workflow_steps(*steps):
    """A valid workflow in the parser's subset whose one job runs exactly `steps` (each without its leading `- `)."""
    body = "".join("      - %s\n" % step for step in steps)
    return """name: t
on:
  issues:
    types: [opened]
permissions:
  contents: read
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
%s""" % body


def workflow_step(step):
    """The same with the pinned checkout first and one step under test."""
    return workflow_steps("uses: actions/checkout@" + SHA, step)


class StrictParseTest(unittest.TestCase):
    """Every construct the parser does not model fails the test instead of being skipped (the Codex bypasses)."""

    def assert_syntax_error(self, text):
        with self.assertRaises(WorkflowSyntaxError):
            parse_workflow(text)

    def test_a_quoted_key(self):
        self.assert_syntax_error(workflow_step('"uses": actions/checkout@v4'))

    def test_a_quoted_key_in_a_mapping(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass").replace("permissions:", '"permissions":'))

    def test_a_space_before_the_colon(self):
        self.assert_syntax_error(workflow_step("uses : actions/labeler@v5"))

    def test_a_quoted_scalar(self):
        self.assert_syntax_error(workflow_step('run: "python3 -c pass"'))

    def test_a_flow_mapping(self):
        self.assert_syntax_error(workflow_step("run: {value: text}"))

    def test_an_anchor(self):
        self.assert_syntax_error(workflow_step("uses: &pin actions/labeler@8558fd74291d67161a8a78ce36a881fa63b766a9"))

    def test_an_alias(self):
        self.assert_syntax_error(workflow_step("uses: *pin"))

    def test_a_tag(self):
        self.assert_syntax_error(workflow_step("run: !plain python3 -c pass"))

    def test_a_folded_block(self):
        self.assert_syntax_error(workflow_step("run: >\n          python3 -c pass"))

    def test_a_chomping_indicator(self):
        self.assert_syntax_error(workflow_step("run: |-\n          python3 -c pass"))

    def test_a_second_document(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass") + "\n---\nother: 1\n")

    def test_a_tab_indent(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass").replace("    steps:", "\tsteps:"))

    def test_a_duplicate_key(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass") + "\npermissions:\n  contents: read\n")

    def test_a_line_that_is_not_a_key(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass").replace("permissions:", "just text"))

    def test_a_nested_mapping_in_a_scalar(self):
        self.assert_syntax_error(workflow_step("run: echo a: b"))

    def test_a_deeper_undocumented_indent(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass").replace("    steps:", "     steps:"))

    def test_the_flow_sequence_item_must_be_plain(self):
        self.assert_syntax_error(workflow_step("run: python3 -c pass").replace("types: [opened]",
                                                                              'types: ["opened"]'))


class BypassTest(unittest.TestCase):
    """Valid subset YAML and expression spellings the invariants must still catch."""

    def test_an_unpinned_action_in_a_later_step(self):
        doc = parse_workflow(workflow_step("uses: actions/setup-go@v5"))
        with self.assertRaises(AssertionError):
            assert_pinned_actions(doc)

    def test_a_job_level_reusable_workflow_is_checked(self):
        text = workflow_step("run: python3 -c pass") + "  call:\n    uses: owner/repo/.github/workflows/x.yml@main\n"
        doc = parse_workflow(text)
        with self.assertRaises(AssertionError):
            assert_pinned_actions(doc)

    def test_a_checkout_hidden_at_job_level(self):
        text = workflow_step("run: python3 -c pass") + "  call:\n    uses: actions/checkout@" + SHA + "\n"
        doc = parse_workflow(text)
        with self.assertRaises(AssertionError):
            assert_no_checkout(doc)

    def test_an_expression_without_spaces(self):
        doc = parse_workflow(workflow_step("run: echo '${{github.event.issue.title}}'"))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_an_expression_with_the_trim_marker(self):
        doc = parse_workflow(workflow_step("run: echo \"${{- github.event.issue.number }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_an_expression_with_bracket_access(self):
        doc = parse_workflow(workflow_step("run: echo \"${{ github.event['issue'].title }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_a_checkout_in_another_case(self):
        doc = parse_workflow(workflow_steps("uses: Actions/Checkout@" + SHA))
        with self.assertRaises(AssertionError):
            assert_no_checkout(doc)

    def test_a_whitespace_separated_expression(self):
        doc = parse_workflow(workflow_step("run: echo \"${{ github . event . issue . title }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_a_bracket_notation_expression(self):
        doc = parse_workflow(workflow_step("run: echo \"${{ github['event']['issue']['title'] }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_an_uppercase_expression(self):
        doc = parse_workflow(workflow_step("run: echo \"${{ GITHUB.EVENT.issue.title }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_event_name_is_not_the_event(self):
        doc = parse_workflow(workflow_step("run: echo \"${{ github.event_name }} ${{ github['event_name'] }}\""))
        assert_no_event_expression(doc)

    def test_an_expression_in_a_literal_block(self):
        doc = parse_workflow(workflow_step("run: |\n          echo \"${{ github.event.issue.body }}\""))
        with self.assertRaises(AssertionError):
            assert_no_event_expression(doc)

    def test_a_job_level_permissions_block(self):
        text = workflow_step("run: python3 -c pass").replace(
            "  j:\n", "  j:\n    permissions:\n      contents: read\n", 1)
        doc = parse_workflow(text)
        with self.assertRaises(AssertionError):
            assert_permissions(doc, {"contents": "read", "issues": "write"})

    def test_a_top_level_permissions_scalar(self):
        doc = parse_workflow(workflow_step("run: python3 -c pass").replace(
            "permissions:\n  contents: read", "permissions: write-all"))
        with self.assertRaises(AssertionError):
            assert_permissions(doc, {"contents": "read", "issues": "write"})


if __name__ == "__main__":
    unittest.main()
