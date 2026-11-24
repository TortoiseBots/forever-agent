#!/usr/bin/env python3
"""Verify that the user manual in docs/manual is a well-formed OKF v0.2 bundle.

The manual is a knowledge bundle in the Open Knowledge Format (OKF) v0.2: a
directory of markdown concept pages, each with a small YAML frontmatter block,
with reserved index.md (directory listings) and log.md (update history) files.
This script is the repository's own checker for the subset of the format the
manual uses. It needs nothing but Python 3 standard library.

Usage:
    python3 tools/verify_okf.py [--root REPO_ROOT]

REPO_ROOT defaults to the parent directory of the tools/ directory that holds
this script. The bundle is REPO_ROOT/docs/manual.

It prints one line per problem (path:line: message) and exits 1 when it found
any; a clean bundle prints a one-line summary and exits 0.

Rules
-----
Concept pages (every .md file except index.md and log.md):
  * a YAML frontmatter block opened by a line "---" at the top of the file and
    closed by another "---" line, parseable by the small parser below (top
    level "key: value" pairs, inline maps and lists, and lists of maps);
  * "type" is one of the types the manual uses;
  * "title" is present and not empty;
  * "description" is present, not empty and fits on one line;
  * "generated.by" is present and "generated.at" is an ISO 8601 UTC timestamp
    (YYYY-MM-DDTHH:MM:SSZ);
  * "status" is one of draft, stable or deprecated.

index.md (every directory):
  * every directory under the bundle root has one, the root included;
  * no frontmatter, except the bundle root, which may carry only okf_version;
  * lists every concept page and every subdirectory of its own directory
    exactly once, through a link that resolves.

Non-Markdown files:
  * the bundle holds only .md files; any other file is a problem (assets
    live outside the bundle).

log.md (every directory):
  * no frontmatter;
  * every "## " heading is an ISO 8601 date, YYYY-MM-DD, and the dates run
    newest first.

Links (every file in the bundle):
  * every relative link or image resolves: a link that stays inside the bundle
    must hit a page or directory of the bundle, a link that leaves the bundle
    must hit a file of the repository;
  * "#anchor" links and absolute URLs are not checked.

Privacy (every file in the bundle, and nothing outside it):
  * no IPv4 address except loopback (127.0.0.0/8); the agent's own defaults
    are loopback addresses, and every other address is a real host somewhere;
  * no absolute home directory path and no Windows drive path;
  * no assigned secret value: password/passwd/secret/token keys with a quoted or
    unquoted value, in a flag, shell assignment, JSON or YAML. A placeholder
    (<password>, PASSWORD, $ENV, ...) is fine, and a *_file/*_env/*_path key
    names a place or a variable, not a secret.

Commands (fenced code blocks in every .md page):
  * a line that starts with forever-agent (optionally behind a path or a shell
    prompt) names a registered top-level command, and one under "forever" names
    a registered forever subcommand. The registrations are read from
    cmd/forever-agent/main.go and cmd/forever-agent/forever_login.go with the
    CASE_LABEL regex (the case labels of switch args[0]).

Reserved names: "index.md" and "log.md" may not be concept pages (OKF 3.1).
"""

import datetime
import os
import posixpath
import re
import sys

BUNDLE = os.path.join("docs", "manual")

RESERVED = {"index.md", "log.md"}

TYPES = (
    "Guide",
    "Command",
    "Method Reference",
    "Concept",
    "Reference",
    "Playbook",
    "Troubleshooting",
)

STATUSES = ("draft", "stable", "deprecated")

# A markdown inline link or image; the target ends at whitespace or ")".
LINK = re.compile(r"!?\[[^\]]*\]\(\s*(<[^>]*>|[^)\s]+)")

# "## 2026-11-24", the only date heading form log.md accepts.
DATE_HEADING = re.compile(r"^##\s+(\d{4}-\d{2}-\d{2})\s*$")
HEADING = re.compile(r"^##\s")

GENERATED_AT = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")

IPV4 = re.compile(r"(?<![\w.])((?:\d{1,3}\.){3}\d{1,3})(?!\w|\.\d)")
HOME_PATH = re.compile(r"/home/")
WINDOWS_PATH = re.compile(r"(?<![A-Za-z])[A-Za-z]:\\")

# A secret-like key and its assigned value, in the forms the manual can carry: a flag (--password=x),
# a shell assignment (PASSWORD=x, export DB_PASSWORD=x), JSON/YAML (password: x, "password": "x").
# The key match starts after a non-word character so --password=x is seen (the dash is allowed in the
# key, and a word before it is not).
SECRET_ASSIGNMENT = re.compile(
    r"(?<![\w-])(?P<key>[A-Za-z0-9_-]*(?:password|passwd|secret|token)[A-Za-z0-9_-]*)"
    r'\s*["\']?\s*[:=]\s*'
    r'(?P<value>"[^"]*"|\'[^\']*\'|[^\s,;)\]}]+)',
    re.IGNORECASE,
)
# The value of one of these keys is a place, a variable name or an identifier, not the secret itself.
SECRET_KEY_SUFFIX = re.compile(r"(?i)(file|env|path|dir)$")
# Placeholder values the manual is allowed to show. Everything else must match a form in _placeholder_value.
PLACEHOLDER_VALUES = {"...", "REDACTED", "redacted"}

# The top-level commands are the case labels of the switch on args[0] in cmd/forever-agent/main.go;
# the forever subcommands are the case labels of the switch on args[0] in foreverCmd, in
# cmd/forever-agent/forever_login.go. Both are read with this one regex, so a command the code does
# not register cannot appear in an example.
CASE_LABEL = re.compile(r'^\s*case "([^"]+)":')
ARGS0_SWITCH = "switch args[0] {"

# A line that starts a forever-agent command, optionally behind a shell prompt or a path.
COMMAND_LINE = re.compile(r"^(?:[$>]\s*)?(?P<path>(?:[^\s/]+/)*forever-agent)(?:\s+(?P<rest>.*))?$")
# Commands are words: a line such as "forever-agent 0.1.0" is output, not a command.
COMMAND_WORD = re.compile(r"^[a-z][a-z0-9-]*$")

KEY = re.compile(r"^([A-Za-z_][A-Za-z0-9_-]*):(?:\s+(.*))?$")


class FrontmatterError(ValueError):
    """The frontmatter block is not in the YAML subset this checker parses."""


def _strip_quotes(value):
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def _split_flow(text):
    """Split the inside of a flow list or mapping on its top-level commas."""
    parts = []
    current = []
    depth = 0
    quote = None
    for ch in text:
        if quote:
            current.append(ch)
            if ch == quote:
                quote = None
            continue
        if ch in "\"'":
            quote = ch
            current.append(ch)
            continue
        if ch in "[{":
            depth += 1
        elif ch in "]}":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(current))
            current = []
            continue
        current.append(ch)
    if "".join(current).strip():
        parts.append("".join(current))
    return parts


def _parse_inline(text):
    """Parse a flow mapping, a flow list or a plain scalar."""
    text = text.strip()
    if text.startswith("{") and text.endswith("}"):
        result = {}
        for part in _split_flow(text[1:-1]):
            if ":" not in part:
                raise FrontmatterError("flow mapping item without a colon: %r" % part.strip())
            key, value = part.split(":", 1)
            result[key.strip()] = _parse_inline(value)
        return result
    if text.startswith("[") and text.endswith("]"):
        return [_parse_inline(part) for part in _split_flow(text[1:-1])]
    return _strip_quotes(text)


def _indent(line):
    return len(line) - len(line.lstrip(" "))


def _parse_child(lines, pos, parent_indent):
    """Parse the block that follows a key or a bare "-" item."""
    while pos < len(lines) and not lines[pos].strip():
        pos += 1
    if pos >= len(lines):
        return None, pos
    indent = _indent(lines[pos])
    if indent <= parent_indent:
        return None, pos
    if lines[pos].lstrip().startswith("-"):
        return _parse_list(lines, pos, indent)
    return _parse_map(lines, pos, indent)


def _parse_map(lines, pos, indent):
    result = {}
    while pos < len(lines):
        line = lines[pos]
        if not line.strip():
            pos += 1
            continue
        current = _indent(line)
        if current < indent:
            break
        if current > indent:
            raise FrontmatterError("unexpected indentation: %r" % line)
        match = KEY.match(line[indent:])
        if not match:
            raise FrontmatterError("not a key/value line: %r" % line.strip())
        key = match.group(1)
        rest = (match.group(2) or "").strip()
        if key in result:
            raise FrontmatterError("duplicate key %r" % key)
        pos += 1
        if rest:
            result[key] = _parse_inline(rest)
        else:
            value, pos = _parse_child(lines, pos, indent)
            result[key] = value
    return result, pos


def _parse_list(lines, pos, indent):
    items = []
    while pos < len(lines):
        line = lines[pos]
        if not line.strip():
            pos += 1
            continue
        current = _indent(line)
        if current < indent:
            break
        if current > indent:
            raise FrontmatterError("unexpected indentation: %r" % line)
        rest = line[indent:]
        if not rest.startswith("-"):
            break
        rest = rest[1:].strip()
        pos += 1
        if not rest:
            value, pos = _parse_child(lines, pos, indent)
            items.append(value)
            continue
        match = KEY.match(rest)
        if match and rest[0] not in "\"'[{":
            item = {}
            key = match.group(1)
            value = (match.group(2) or "").strip()
            if value:
                item[key] = _parse_inline(value)
            else:
                child, pos = _parse_child(lines, pos, indent)
                item[key] = child
            while pos < len(lines):
                following = lines[pos]
                if not following.strip():
                    pos += 1
                    continue
                deeper = _indent(following)
                if deeper <= indent:
                    break
                extra, pos = _parse_map(lines, pos, deeper)
                item.update(extra)
            items.append(item)
        else:
            items.append(_parse_inline(rest))
    return items, pos


def parse_frontmatter(text):
    """Return (data, body_start_line) for a file that opens with frontmatter.

    Raises FrontmatterError when the file has no frontmatter or the block is not
    in the YAML subset above.
    """
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        raise FrontmatterError("no frontmatter block at the top of the file")
    end = None
    for index in range(1, len(lines)):
        if lines[index].strip() == "---":
            end = index
            break
    if end is None:
        raise FrontmatterError("frontmatter block is not closed by a --- line")
    block = lines[1:end]
    data = {}
    if block:
        data, pos = _parse_map(block, 0, 0)
        if any(line.strip() for line in block[pos:]):
            raise FrontmatterError("frontmatter has content that is not a key/value pair")
    return data, end + 2


def _nonempty(value):
    return isinstance(value, str) and bool(value.strip())


def _check_concept(rel, data, report):
    page_type = data.get("type")
    if not _nonempty(page_type):
        report(rel, 0, "frontmatter has no type")
    elif page_type not in TYPES:
        report(rel, 0, "unknown type %r (one of: %s)" % (page_type, ", ".join(TYPES)))

    if not _nonempty(data.get("title")):
        report(rel, 0, "frontmatter has no title")

    description = data.get("description")
    if not _nonempty(description):
        report(rel, 0, "frontmatter has no description")
    elif "\n" in description or "\r" in description:
        report(rel, 0, "description is not one line")

    generated = data.get("generated")
    if not isinstance(generated, dict):
        report(rel, 0, "frontmatter has no generated { by, at } block")
    else:
        if not _nonempty(generated.get("by")):
            report(rel, 0, "generated block has no by")
        at = generated.get("at")
        if not _nonempty(at):
            report(rel, 0, "generated block has no at")
        elif not GENERATED_AT.match(at):
            report(rel, 0, "generated.at %r is not an ISO 8601 UTC timestamp (YYYY-MM-DDTHH:MM:SSZ)" % at)
        else:
            try:
                datetime.datetime.strptime(at, "%Y-%m-%dT%H:%M:%SZ")
            except ValueError:
                report(rel, 0, "generated.at %r is not a real date and time" % at)

    status = data.get("status")
    if status is None:
        report(rel, 0, "frontmatter has no status")
    elif status not in STATUSES:
        report(rel, 0, "unknown status %r (one of: %s)" % (status, ", ".join(STATUSES)))


def _check_log(rel, text, report):
    dates = []
    seen_title = False
    for number, line in enumerate(text.splitlines(), start=1):
        if not HEADING.match(line):
            if line.startswith("# ") and not seen_title:
                seen_title = True
            continue
        match = DATE_HEADING.match(line)
        if not match:
            report(rel, number, "log date heading is not ## YYYY-MM-DD: %r" % line.strip())
            continue
        try:
            datetime.date.fromisoformat(match.group(1))
        except ValueError:
            report(rel, number, "log date %r is not a real date" % match.group(1))
            continue
        dates.append((number, match.group(1)))
    if not dates:
        report(rel, 0, "log has no ## YYYY-MM-DD date heading")
        return
    for (prev_line, prev), (line, current) in zip(dates, dates[1:]):
        if current >= prev:
            report(rel, line, "log date %s is not before %s on line %d (newest first)" % (current, prev, prev_line))


def _links(text):
    """Yield (line number, target) for every inline markdown link or image."""
    for number, line in enumerate(text.splitlines(), start=1):
        for match in LINK.finditer(line):
            yield number, match.group(1)


def _resolve(page_rel, target):
    """Resolve a link target to a repository-relative path, or None to skip."""
    if target.startswith("<") and target.endswith(">"):
        target = target[1:-1]
    target = target.strip()
    if not target or target.startswith("#"):
        return None
    if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", target) or target.startswith("//"):
        return None
    target = target.split("#", 1)[0].split("?", 1)[0]
    if not target:
        return None
    if target.startswith("/"):
        joined = posixpath.join(BUNDLE, target.lstrip("/"))
    else:
        joined = posixpath.join(posixpath.dirname(page_rel), target)
    return posixpath.normpath(joined)


def _check_links(rel, text, root, report):
    for number, target in _links(text):
        resolved = _resolve(rel, target)
        if resolved is None:
            continue
        if resolved == ".." or resolved.startswith("../"):
            report(rel, number, "link leaves the repository: %s" % target)
            continue
        if not os.path.exists(os.path.join(root, resolved)):
            report(rel, number, "link does not resolve: %s (looked for %s)" % (target, resolved))


def _placeholder_value(value):
    """True for a value the manual may show in place of a secret."""
    value = _strip_quotes(value.strip())
    if not value or value in PLACEHOLDER_VALUES:
        return True
    if value.startswith("<") and value.endswith(">"):
        return True
    if value.startswith("$") or value.startswith("`"):
        return True
    if value.startswith("%") and value.endswith("%"):
        return True
    if value.startswith("-"):
        # Prose such as "no password: --pass-file ..." names the flag that carries the password.
        return True
    if re.fullmatch(r"[A-Z][A-Z0-9_]*", value):
        return True
    return False


def _check_privacy(rel, text, report):
    for number, line in enumerate(text.splitlines(), start=1):
        for match in IPV4.finditer(line):
            address = match.group(1)
            if address.startswith("127."):
                continue
            report(rel, number, "IPv4 address in the manual: %s" % address)
        if HOME_PATH.search(line):
            report(rel, number, "absolute home directory path: %s" % line.strip()[:120])
        if WINDOWS_PATH.search(line):
            report(rel, number, "Windows drive path: %s" % line.strip()[:120])
        for match in SECRET_ASSIGNMENT.finditer(line):
            if SECRET_KEY_SUFFIX.search(match.group("key")):
                continue
            if _placeholder_value(match.group("value")):
                continue
            report(rel, number, "secret value in the manual: %s" % match.group(0)[:120])


def _switch_commands(text, marker):
    """The case labels of the first switch on args[0] after marker, one per line."""
    start = text.find(marker)
    if start < 0:
        return set()
    start = text.find(ARGS0_SWITCH, start)
    if start < 0:
        return set()
    names = set()
    depth = 0
    for line in text[start:].splitlines():
        depth += line.count("{") - line.count("}")
        match = CASE_LABEL.match(line)
        if match:
            names.add(match.group(1))
        if depth <= 0 and names:
            break
    return names


def parse_command_registry(main_text, forever_text):
    """Return (top-level commands, forever subcommands) parsed from the two Go files' switches."""
    return _switch_commands(main_text, "func run("), _switch_commands(forever_text, "func foreverCmd(")


def load_command_registry(root):
    """Read cmd/forever-agent's registration, or None when that tree is not under root."""
    paths = [os.path.join(root, "cmd", "forever-agent", name) for name in ("main.go", "forever_login.go")]
    try:
        texts = []
        for path in paths:
            with open(path, encoding="utf-8") as handle:
                texts.append(handle.read())
    except OSError:
        return None
    return parse_command_registry(*texts)


def _check_commands(rel, text, registry, report):
    top, forever = registry
    in_fence = False
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if not in_fence:
            continue
        match = COMMAND_LINE.match(stripped)
        if not match:
            continue
        rest = (match.group("rest") or "").split()
        if not rest or not COMMAND_WORD.match(rest[0]):
            continue
        command = rest[0]
        if command not in top:
            report(rel, number, "unknown forever-agent command: %s" % command)
            continue
        if command == "forever" and len(rest) >= 2 and COMMAND_WORD.match(rest[1]) and rest[1] not in forever:
            report(rel, number, "unknown forever subcommand: %s" % rest[1])


def _children(directory):
    """Names of the concept pages and subdirectories an index must list."""
    names = []
    for entry in sorted(os.listdir(directory)):
        full = os.path.join(directory, entry)
        if os.path.isdir(full):
            names.append(entry + "/")
        elif entry.endswith(".md") and entry not in RESERVED:
            names.append(entry)
    return names


def _check_index(rel, text, root, is_root, report):
    if text.splitlines() and text.splitlines()[0].strip() == "---":
        if not is_root:
            report(rel, 1, "index.md must not carry frontmatter")
        else:
            try:
                data, _ = parse_frontmatter(text)
            except FrontmatterError as error:
                report(rel, 1, "frontmatter does not parse: %s" % error)
                return
            extra = sorted(set(data) - {"okf_version"})
            if extra:
                report(rel, 1, "bundle-root frontmatter may carry only okf_version, found %s" % ", ".join(extra))
            if "okf_version" in data and not _nonempty(data["okf_version"]):
                report(rel, 1, "okf_version is empty")

    listed = {}
    for number, target in _links(text):
        resolved = _resolve(rel, target)
        if resolved is None:
            continue
        listed.setdefault(resolved, []).append(number)

    directory = posixpath.dirname(rel)
    for name in _children(os.path.join(root, directory)):
        expected = posixpath.normpath(posixpath.join(directory, name))
        lines = listed.get(expected, [])
        if not lines:
            report(rel, 0, "index does not list %s" % name)
        elif len(lines) > 1:
            report(rel, lines[1], "index lists %s more than once (first on line %d)" % (name, lines[0]))


def check_bundle(root):
    """Return the problems in the bundle at root/docs/manual, one string each."""
    problems = []

    def report(rel, line, message):
        problems.append("%s:%d: %s" % (rel, line, message) if line else "%s: %s" % (rel, message))

    bundle = os.path.join(root, BUNDLE)
    if not os.path.isdir(bundle):
        return ["%s: bundle directory not found" % posixpath.join(BUNDLE)]

    registry = load_command_registry(root)
    for directory, dirs, files in os.walk(bundle):
        dirs.sort()
        names = set(files)
        rel_dir = posixpath.join(*os.path.relpath(directory, root).split(os.sep))
        if "index.md" not in names:
            report(posixpath.join(rel_dir, "index.md"), 0, "directory has no index.md")
        for name in sorted(files):
            full = os.path.join(directory, name)
            rel = posixpath.join(*os.path.relpath(full, root).split(os.sep))
            if not name.endswith(".md"):
                report(rel, 0, "not a Markdown file: the bundle holds only .md pages (assets live outside it)")
                with open(full, encoding="utf-8", errors="replace") as handle:
                    text = handle.read()
                _check_privacy(rel, text, report)
                continue
            with open(full, encoding="utf-8") as handle:
                text = handle.read()
            is_root = rel == posixpath.join(BUNDLE, "index.md")

            if name == "index.md":
                _check_index(rel, text, root, is_root, report)
            elif name == "log.md":
                if text.splitlines() and text.splitlines()[0].strip() == "---":
                    report(rel, 1, "log.md must not carry frontmatter")
                _check_log(rel, text, report)
            else:
                try:
                    data, _ = parse_frontmatter(text)
                except FrontmatterError as error:
                    report(rel, 1, "frontmatter does not parse: %s" % error)
                    data = None
                if data is not None:
                    _check_concept(rel, data, report)

            _check_links(rel, text, root, report)
            _check_privacy(rel, text, report)
            if registry is not None:
                _check_commands(rel, text, registry, report)

    return problems


def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    args = list(argv[1:])
    while args:
        arg = args.pop(0)
        if arg in ("-h", "--help"):
            print(__doc__.strip())
            return 0
        if arg == "--root" and args:
            root = os.path.abspath(args.pop(0))
            continue
        print("usage: verify_okf.py [--root REPO_ROOT]", file=sys.stderr)
        return 2

    problems = check_bundle(root)
    for problem in problems:
        print(problem)
    if problems:
        print("verify_okf: %d problem(s)" % len(problems), file=sys.stderr)
        return 1

    pages = indexes = logs = 0
    bundle = os.path.join(root, BUNDLE)
    for directory, _, files in os.walk(bundle):
        for name in files:
            if name == "index.md":
                indexes += 1
            elif name == "log.md":
                logs += 1
            elif name.endswith(".md"):
                pages += 1
    print("verify_okf: ok (%d concept pages, %d index files, %d log files)" % (pages, indexes, logs))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
