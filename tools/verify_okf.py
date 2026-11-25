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
  * "generated.by" is present, is exactly "forever-agent docs" (the project;
    never a tool, model or person name) and "generated.at" is an ISO 8601 UTC
    timestamp (YYYY-MM-DDTHH:MM:SSZ);
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
  * no IPv4 address except loopback (127.0.0.0/8), and no IPv6 address except
    loopback (::1); the agent's own defaults are loopback addresses, and every
    other address is a real host somewhere;
  * no absolute home directory path (/home/, /root/, /Users/) and no Windows
    drive path; no e-mail address except example.invalid or example.com; no
    host-shaped dotted name outside a short allow-list (github.com, battle.net
    domains, example.invalid, localhost). Host-shaped means a known top-level
    domain, three or more labels, or a machine-looking first label; a file name
    or code symbol with a known suffix is not;
  * no assigned secret value: password/passwd/secret/token keys with a quoted or
    unquoted value, in a flag, shell assignment, JSON or YAML. A placeholder
    (<password>, PASSWORD, $ENV, ...) is fine, and a *_file/*_env/*_path key
    names a place or a variable, not a secret.

Privacy (every Markdown file under docs/, the manual included):
  * no test-result runner field naming a machine: a result line is cut down
    to RESULT and the counts, never the build runner;
  * outside the manual, no private-network IPv4 address (RFC 1918 or
    link-local; the documentation ranges and loopback are fine) and no
    home-directory or Windows drive path. The manual's own rule
    above already refuses every non-loopback address and home path.

Every page is LF-only: a CRLF line ending is a problem.

Commands (fenced code blocks and indented code blocks in every .md page):
  * a line that starts with forever-agent (optionally behind a path or a shell
    prompt) names a registered top-level command, and one under "forever" names
    a registered forever subcommand. The registrations are read from
    cmd/forever-agent/main.go and cmd/forever-agent/forever_login.go by
    tokenizing the switch on args[0] (comments, strings and runes are skipped,
    case labels at the switch's own depth only, several labels per case).

Command coverage (the whole bundle):
  * every command the binary registers is documented in at least one manual
    page as a command: a line in a code block ("forever-agent <word>", or
    "forever-agent forever <word>" for a subcommand), a heading naming it, a
    table cell naming the command form ("forever-agent <word>" or
    "forever <word>"), or a bare backticked word in a table cell under a header
    that names the command column ("Command", "Subcommand"). A bare backticked
    word in prose, or in any other table cell, does not count;
  * docs/manual/.undocumented-commands holds the names deliberately left out
    (hidden or debug commands), one "name # reason" per line; an entry that is
    not a registered command is a problem, so the list cannot rot.

Method coverage (a serve's method table against docs/manual/daemon):
  * the registered methods are read from internal/forever/world/*.go (never a
    _test.go file) with three patterns, all inside that package: the string
    argument of a call "srv.Register(\"name\"", the string keys of a map
    literal that a "for name, fn := range map[string]..." walks and registers
    with "srv.Register(name", and the name fields of the refusedMethods slice;
  * every implemented method is a row of docs/manual/daemon/method-index.md,
    and every method named in a row of that page is registered; a method the
    page leaves out on purpose is a "name # reason" line of
    docs/manual/.undocumented-methods, and an entry there that names no
    registered method is a problem;
  * every refusedMethods name is in docs/manual/daemon/not-ported-methods.md,
    and every method named in that page's names table is still refused.

Frontmatter sources: every sources[].resource (and a top-level resource) that
is a repository-relative path must exist.

Links: an anchor (#heading) on a link to a bundle page must match a heading of
that page. An index entry's description must equal the page's frontmatter
description.

Reserved names: "index.md" and "log.md" may not be concept pages (OKF 3.1).
Names that start with a dot (the two allow-lists) are not pages.
"""

import datetime
import ipaddress
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

# generated.by always names the project: the page belongs to the manual, not to
# whatever tool, model or person last wrote it.
GENERATED_BY = "forever-agent docs"

IPV4 = re.compile(r"(?<![\w.])((?:\d{1,3}\.){3}\d{1,3})(?!\w|\.\d)")
HOME_PATH = re.compile(r"/home/")
ROOT_PATH = re.compile(r"/root/")
USERS_PATH = re.compile(r"/Users/")
WINDOWS_PATH = re.compile(r"(?<![A-Za-z])[A-Za-z]:\\")

# A test result line's build runner: the runner field names the machine a test ran on, and a public repository never
# publishes it (a quoted result is cut down to RESULT and the counts).
RUNNER_FIELD = re.compile(r"(?<![\w-])" + "runner" + "=" + r"([A-Za-z0-9][A-Za-z0-9_.-]*)")
# The private networks the tree-wide scan refuses: RFC 1918 and link-local IPv4. Documentation ranges (192.0.2.0/24,
# 198.51.100.0/24, 203.0.113.0/24) and the unspecified address 0.0.0.0 are placeholders, not a private host.
PRIVATE_NETWORKS = tuple(
    ipaddress.ip_network(net) for net in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16")
)
# Files under docs/ that are not text: the tree-wide privacy scan skips them.
BINARY_SUFFIXES = {".jpg", ".jpeg", ".png", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz", ".tar"}

# An e-mail address; example.invalid and example.com are the placeholders the manual may show.
EMAIL = re.compile(r"(?<![\w.+-])([A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,}))(?![\w-])")
EMAIL_DOMAINS = ("example.invalid", "example.com")

# A host name: a dotted name whose last label is alphabetic. The allow-list holds the domains the docs
# legitimately link to. Everything else that is host-shaped is reported: a last label that is a known
# top-level domain, three or more labels (a fully qualified name), or a first label carrying a digit or
# a hyphen (a machine name such as build01.corp). A file name or a code symbol has a known non-host
# suffix, or a plain identifier as its first label, so it is not reported.
HOST_SHAPED = re.compile(
    r"(?<![A-Za-z0-9_.-])"
    r"((?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+([A-Za-z]{2,}))"
    r"(?![A-Za-z0-9_-]|\.[A-Za-z0-9])"
)
HOST_TLDS = {
    "com", "net", "org", "io", "invalid", "local", "dev", "gg", "tv", "xyz", "me", "co", "uk",
    "biz", "info", "app", "cloud", "host", "site", "online", "tech", "store", "page", "link", "ai",
    "internal", "lan", "home", "corp", "arpa", "test", "example", "intranet", "private", "localdomain",
}
# A dotted name's last label that names a file or a code artifact, not a host: a manual names its own
# files and Go packages far more often than a host.
NON_HOST_SUFFIXES = {
    "md", "go", "json", "jsonl", "txt", "tsv", "sh", "pem", "log", "yml", "yaml", "mmap", "mmtile",
    "svg", "sql", "db2", "dbc", "conf", "inc", "cpp", "hpp", "h", "lua", "png", "jpg", "jpeg", "gif",
    "gz", "tar", "zip", "diff", "lock", "js", "css", "xml", "csv", "ini", "toml", "rst", "html",
    "htm", "py", "rb", "php", "cs", "java", "proto", "bin", "dat", "db", "o", "so", "a", "exe",
    "bat", "ps1", "twcap", "pid", "token", "party", "mod",
}
ALLOWED_HOSTS = ("github.com", "battle.net", "example.invalid", "example.com")
ALLOWED_HOST_SUFFIXES = (".github.com", ".battle.net", ".example.invalid", ".example.com")
LOCAL_HOSTS = ("localhost",)

# An IPv6 literal: the token is handed to the standard library, so a time (12:30:00) or a scope
# operator (Player::SendAddonMessage) is not one. Loopback (::1) is the one address the manual may show.
IPV6_CANDIDATE = re.compile(r"(?<![0-9A-Za-z:])[0-9A-Fa-f:]*[0-9A-Fa-f][0-9A-Fa-f:]*(?![0-9A-Za-z:])")

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
# cmd/forever-agent/forever_login.go. The switch is tokenized (comments and string/rune literals are
# skipped) and its labels are read only at the switch's own brace depth, so a nested switch's case and
# a "case" line inside a string are not commands. A case may name several labels: case "a", "b":.
COMMAND_LINE = re.compile(r"^(?:[$>]\s*)?(?P<path>(?:[^\s/]+/)*forever-agent)(?:\s+(?P<rest>.*))?$")
# Commands are words: a line such as "forever-agent 0.1.0" is output, not a command.
COMMAND_WORD = re.compile(r"^[a-z][a-z0-9-]*$")
# A heading or a table row documents a command only when it names the command form
# ("forever-agent <word>", "forever-agent forever <word>" or "forever <word>"); a bare backticked
# word in prose does not.
COMMAND_FORM = re.compile(r"^(?:forever-agent\s+(?:forever\s+)?|forever\s+)([a-z][a-z0-9-]*)\b")
MD_HEADING = re.compile(r"^\s{0,3}#{1,6}\s+(.*)$")
FENCE = re.compile(r"^(`{3,}|~{3,})")

# The command-coverage allow-list and the method-coverage allow-list: names deliberately left unwritten, one
# "name # reason" per line. They live inside the bundle but start with a dot, so they are not pages.
UNDOCUMENTED_COMMANDS = posixpath.join(BUNDLE, ".undocumented-commands")
UNDOCUMENTED_METHODS = posixpath.join(BUNDLE, ".undocumented-methods")

# A backticked phrase: "forever serve", "forever-agent forever serve --dir ./slot".
BACKTICK = re.compile(r"`([^`\n]+)`")

# A method-index table row: the first column is the method in backticks.
METHOD_ROW = re.compile(r"^\|\s*`([A-Za-z0-9_]+)`\s*\|")

# The serve registers a method by name in these shapes, all inside internal/forever/world:
#   srv.Register("status", ...)                       a literal name;
#   for name, fn := range map[string]...{ "mail_list": ... } { srv.Register(name, ...) }
#                                                     a map or []string literal a range walks;
#   var refusedMethods = []serveRefusal{ {name: "tt", why: ...}, ... }
#                                                     the old daemon's names this server refuses,
#                                                     registered through the loop's local alias.
# A call whose first argument the checker cannot read is an error, never a silent skip.
REFUSED_LIST = re.compile(r"var\s+refusedMethods\s*=\s*\[\]serveRefusal\s*\{(.*?)\n\}", re.S)
REFUSED_NAME = re.compile(r'\bname:\s*"([^"\n]+)"')
NOT_PORTED_LIST = re.compile(r"var\s+notPortedMethods\s*=\s*\[\]string\s*\{(.*?)\}", re.S)

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


def _is_repo_path(value):
    """True for a repository-relative path (not a URL, an absolute path or a placeholder)."""
    if not _nonempty(value):
        return False
    value = value.strip()
    if value.startswith(("http://", "https://", "//", "/", "<")):
        return False
    if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", value):
        return False
    return True


def _check_sources(rel, data, root, report):
    """Every sources[].resource (and a top-level resource) that is a repo path must exist."""
    resources = []
    sources = data.get("sources")
    if isinstance(sources, list):
        for source in sources:
            if isinstance(source, dict) and "resource" in source:
                resources.append(source["resource"])
    if "resource" in data:
        resources.append(data["resource"])
    for resource in resources:
        if not _is_repo_path(resource):
            continue
        path = resource.strip()
        if not os.path.exists(os.path.join(root, path)):
            report(rel, 0, "source resource does not exist: %s" % path)


def _check_concept(rel, data, root, report):
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
        elif generated.get("by") != GENERATED_BY:
            report(
                rel,
                0,
                "generated.by %r must be %r (the project, never a tool, model or person name); "
                "write 'generated: { by: %s, at: <timestamp> }'" % (generated.get("by"), GENERATED_BY, GENERATED_BY),
            )
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

    _check_sources(rel, data, root, report)


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


def _slugs(text):
    """The GitHub-style heading anchors of a page (duplicates get -1, -2, ...)."""
    slugs = set()
    counts = {}
    fence = None
    for line in text.splitlines():
        stripped = line.strip()
        match = FENCE.match(stripped)
        if fence is not None:
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= fence[1]:
                fence = None
            continue
        if match:
            fence = (match.group(1)[0], len(match.group(1)))
            continue
        heading = MD_HEADING.match(line)
        if not heading:
            continue
        title = heading.group(1).replace("`", "").strip()
        slug = re.sub(r"[^\w\- ]", "", title.lower(), flags=re.UNICODE).strip().replace(" ", "-")
        count = counts.get(slug, 0)
        counts[slug] = count + 1
        slugs.add(slug if count == 0 else "%s-%d" % (slug, count))
    return slugs


def _check_links(rel, text, root, report):
    for number, target in _links(text):
        if target.startswith("<") and target.endswith(">"):
            target = target[1:-1]
        target = target.strip()
        if not target or target.startswith("//"):
            continue
        if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", target):
            continue
        fragment = None
        if "#" in target:
            target, fragment = target.split("#", 1)
        target = target.split("?", 1)[0]
        if not target:
            if fragment and fragment not in _slugs(text):
                report(rel, number, "link anchor does not resolve: #%s" % fragment)
            continue
        resolved = _resolve(rel, target)
        if resolved is None:
            continue
        if resolved == ".." or resolved.startswith("../"):
            report(rel, number, "link leaves the repository: %s" % target)
            continue
        full = os.path.join(root, resolved)
        if not os.path.exists(full):
            report(rel, number, "link does not resolve: %s (looked for %s)" % (target, resolved))
            continue
        if fragment and resolved.endswith(".md") and (resolved + "/").startswith(BUNDLE + "/"):
            page_text = _read_page(root, resolved)
            if page_text is not None and fragment not in _slugs(page_text):
                report(rel, number, "link anchor does not resolve: %s#%s" % (target, fragment))


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


def _allowed_host(host):
    host = host.lower()
    if host in LOCAL_HOSTS or host in ALLOWED_HOSTS:
        return True
    return any(host.endswith(suffix) for suffix in ALLOWED_HOST_SUFFIXES)


def _host_problem(host):
    """True when a dotted name is host-shaped and not on the allow-list."""
    labels = host.lower().split(".")
    if labels[-1] in NON_HOST_SUFFIXES:
        return False
    if labels[-1] in HOST_TLDS:
        return True
    if len(labels) >= 3:
        return True
    return any(char.isdigit() or char == "-" for char in labels[0])


def _ipv6_address(token):
    """The IPv6 address a token spells, or None."""
    if token.count(":") < 2:
        return None
    try:
        return ipaddress.IPv6Address(token)
    except ValueError:
        return None


def _check_privacy(rel, text, report):
    for number, line in enumerate(text.splitlines(), start=1):
        for match in IPV4.finditer(line):
            address = match.group(1)
            if address.startswith("127."):
                continue
            report(rel, number, "IPv4 address in the manual: %s" % address)
        if HOME_PATH.search(line):
            report(rel, number, "absolute home directory path: %s" % line.strip()[:120])
        if ROOT_PATH.search(line):
            report(rel, number, "absolute home directory path: %s" % line.strip()[:120])
        if USERS_PATH.search(line):
            report(rel, number, "absolute home directory path: %s" % line.strip()[:120])
        if WINDOWS_PATH.search(line):
            report(rel, number, "Windows drive path: %s" % line.strip()[:120])
        for match in EMAIL.finditer(line):
            domain = match.group(2).lower()
            if any(domain == allowed or domain.endswith("." + allowed) for allowed in EMAIL_DOMAINS):
                continue
            report(rel, number, "e-mail address in the manual: %s" % match.group(1))
        for match in HOST_SHAPED.finditer(line):
            host = match.group(1)
            if match.start() > 0 and line[match.start() - 1] == "@":
                continue
            if _allowed_host(host) or not _host_problem(host):
                continue
            report(rel, number, "host name in the manual: %s" % host)
        for match in IPV6_CANDIDATE.finditer(line):
            address = _ipv6_address(match.group(0))
            if address is None or address.is_loopback:
                continue
            report(rel, number, "IPv6 address in the manual: %s" % match.group(0))
        for match in SECRET_ASSIGNMENT.finditer(line):
            if SECRET_KEY_SUFFIX.search(match.group("key")):
                continue
            if _placeholder_value(match.group("value")):
                continue
            report(rel, number, "secret value in the manual: %s" % match.group(0)[:120])


def _check_docs_tree_privacy(root, report):
    """The tree-wide privacy rule: any Markdown file under docs/ may not name a build runner, and the files outside
    the manual may not carry a private-network address or a home path either. The manual's own privacy check already
    refuses every non-loopback address and home path, so those files get the runner rule only (no duplicate report)."""
    docs = os.path.join(root, "docs")
    if not os.path.isdir(docs):
        return
    bundle_prefix = BUNDLE + os.sep
    for directory, dirs, files in os.walk(docs):
        dirs[:] = sorted(d for d in dirs if not d.startswith("."))
        for name in sorted(files):
            if not name.endswith(".md") or os.path.splitext(name)[1].lower() in BINARY_SUFFIXES:
                continue
            full = os.path.join(directory, name)
            rel = posixpath.join(*os.path.relpath(full, root).split(os.sep))
            try:
                with open(full, encoding="utf-8", errors="replace") as handle:
                    text = handle.read()
            except OSError:
                continue
            in_bundle = rel.startswith(bundle_prefix)
            for number, line in enumerate(text.splitlines(), start=1):
                for match in RUNNER_FIELD.finditer(line):
                    report(rel, number, "a test-result runner field names a machine under docs: %s" % match.group(1))
                if in_bundle:
                    continue
                for match in IPV4.finditer(line):
                    address = match.group(1)
                    try:
                        parsed = ipaddress.ip_address(address)
                    except ValueError:
                        continue
                    if parsed.is_loopback or not any(parsed in net for net in PRIVATE_NETWORKS):
                        continue
                    report(rel, number, "private-network IPv4 address under docs: %s" % address)
                if HOME_PATH.search(line) or ROOT_PATH.search(line) or USERS_PATH.search(line):
                    report(rel, number, "absolute home directory path under docs: %s" % line.strip()[:120])
                if WINDOWS_PATH.search(line):
                    report(rel, number, "Windows drive path under docs: %s" % line.strip()[:120])


def _go_tokens(text):
    """Yield (kind, value, line) for Go source, skipping comments and keeping literals whole.

    Kinds are "ident", "string", "rune" and "punct". A brace inside a string, a raw string or a
    comment is therefore never counted as a brace.
    """
    tokens = []
    index = 0
    line = 1
    length = len(text)
    while index < length:
        char = text[index]
        if char == "\n":
            line += 1
            index += 1
            continue
        if char in " \t\r":
            index += 1
            continue
        if char == "/" and index + 1 < length and text[index + 1] == "/":
            end = text.find("\n", index)
            index = length if end < 0 else end
            continue
        if char == "/" and index + 1 < length and text[index + 1] == "*":
            end = text.find("*/", index + 2)
            if end < 0:
                break
            line += text.count("\n", index, end)
            index = end + 2
            continue
        if char == '"':
            start_line = line
            index += 1
            value = []
            while index < length:
                char = text[index]
                if char == "\\" and index + 1 < length:
                    value.append({"n": "\n", "t": "\t", "r": "\r"}.get(text[index + 1], text[index + 1]))
                    index += 2
                    continue
                if char == '"':
                    index += 1
                    break
                if char == "\n":
                    line += 1
                value.append(char)
                index += 1
            tokens.append(("string", "".join(value), start_line))
            continue
        if char == "`":
            start_line = line
            end = text.find("`", index + 1)
            if end < 0:
                end = length
            value = text[index + 1:end]
            line += value.count("\n")
            tokens.append(("string", value, start_line))
            index = end + 1
            continue
        if char == "'":
            index += 1
            while index < length:
                if text[index] == "\\":
                    index += 2
                    continue
                if text[index] == "'":
                    index += 1
                    break
                if text[index] == "\n":
                    line += 1
                index += 1
            tokens.append(("rune", "", line))
            continue
        if char.isalpha() or char == "_":
            start = index
            index += 1
            while index < length and (text[index].isalnum() or text[index] == "_"):
                index += 1
            tokens.append(("ident", text[start:index], line))
            continue
        tokens.append(("punct", char, line))
        index += 1
    return tokens


def _switch_commands(text, marker):
    """The case labels of the first switch on args[0] after marker, one per label.

    The token stream is walked with a brace depth; a label counts only at the switch's own depth,
    so a case inside a nested switch or an if block is not a top-level command. A case line may
    name several labels (case "a", "b":); every string literal before the colon is one.
    """
    start = text.find(marker)
    if start < 0:
        return set()
    tokens = _go_tokens(text[start:])
    names = set()
    depth = 0
    index = 0
    body_depth = None
    while index < len(tokens):
        kind, value, _ = tokens[index]
        if kind == "punct" and value == "{":
            depth += 1
            index += 1
            continue
        if kind == "punct" and value == "}":
            depth -= 1
            if body_depth is not None and depth < body_depth:
                break
            index += 1
            continue
        if body_depth is None:
            # switch args[0] {
            if (
                kind == "ident" and value == "switch"
                and index + 5 < len(tokens)
                and tokens[index + 1][1] == "args"
                and tokens[index + 2][1] == "["
                and tokens[index + 3][1] == "0"
                and tokens[index + 4][1] == "]"
                and tokens[index + 5][1] == "{"
            ):
                body_depth = depth + 1
                index += 5
                continue
            index += 1
            continue
        if depth == body_depth and kind == "ident" and value == "case":
            index += 1
            while index < len(tokens):
                kind, value, _ = tokens[index]
                if kind == "string":
                    names.add(value)
                elif kind == "punct" and value in ":}":
                    break
                index += 1
        index += 1
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


def _matching_brace(tokens, opening):
    """The index of the "}" that closes the "{" at opening, or -1."""
    depth = 0
    for index in range(opening, len(tokens)):
        kind, value, _ = tokens[index]
        if kind != "punct":
            continue
        if value == "{":
            depth += 1
        elif value == "}":
            depth -= 1
            if depth == 0:
                return index
    return -1


def _collection_names(tokens, index):
    """The names a `range` at index walks, its last token index and the source variable if any.

    A map[string]T{...} literal yields its string keys, a []string{...} literal its strings. A single
    identifier yields no names but names itself, so a caller can resolve a package-level slice. Any other
    expression yields None: the checker does not guess.
    """
    index += 1
    if index >= len(tokens):
        return None, index, None
    kind, value, _ = tokens[index]
    first_kind, first_value = kind, value
    if kind == "ident" and value == "map":
        opening = index + 1
        while opening < len(tokens) and not (tokens[opening][0] == "punct" and tokens[opening][1] == "{"):
            opening += 1
        if opening >= len(tokens):
            return None, opening, None
        closing = _matching_brace(tokens, opening)
        names = set()
        depth = 0
        for position in range(opening, closing + 1):
            kind, value, _ = tokens[position]
            if kind == "punct" and value == "{":
                depth += 1
                continue
            if kind == "punct" and value == "}":
                depth -= 1
                continue
            if (
                depth == 1
                and kind == "string"
                and position + 1 <= closing
                and tokens[position + 1][0] == "punct"
                and tokens[position + 1][1] == ":"
            ):
                names.add(value)
        return names, closing, None
    if (
        kind == "punct"
        and value == "["
        and index + 3 < len(tokens)
        and tokens[index + 1][1] == "]"
        and tokens[index + 2][0] == "ident"
        and tokens[index + 2][1] == "string"
        and tokens[index + 3][0] == "punct"
        and tokens[index + 3][1] == "{"
    ):
        opening = index + 3
        closing = _matching_brace(tokens, opening)
        names = set()
        depth = 0
        for position in range(opening, closing + 1):
            kind, value, _ = tokens[position]
            if kind == "punct" and value == "{":
                depth += 1
                continue
            if kind == "punct" and value == "}":
                depth -= 1
                continue
            if depth == 1 and kind == "string":
                names.add(value)
        return names, closing, None
    if first_kind == "ident" and index + 1 < len(tokens) and tokens[index + 1][1] == "{":
        # A plain variable: its value is the collection, and the next token opens the loop body.
        return None, index, first_value
    closing = index
    depth = 0
    while closing < len(tokens):
        kind, value, _ = tokens[closing]
        if kind == "punct":
            if value in "([{":
                depth += 1
            elif value in ")]}":
                if depth == 0:
                    break
                depth -= 1
            elif value == ";" and depth == 0:
                break
        closing += 1
    source = first_value if first_kind == "ident" else None
    return None, closing - 1, source


def _range_loops(tokens):
    """Every `for ... := range` loop whose collection this checker can read.

    Returns tuples of (loop variables, names or None, source variable or None, body start, body end).
    """
    loops = []
    for index, (kind, value, _) in enumerate(tokens):
        if kind != "ident" or value != "range":
            continue
        variables = set()
        position = index - 1
        while position >= 0:
            kind, value, _ = tokens[position]
            if kind == "ident" and value == "for":
                break
            if kind == "ident":
                variables.add(value)
            position -= 1
        names, closing, source = _collection_names(tokens, index)
        opening = closing + 1
        while opening < len(tokens) and not (tokens[opening][0] == "punct" and tokens[opening][1] == "{"):
            opening += 1
        body_end = _matching_brace(tokens, opening) if opening < len(tokens) else -1
        loops.append((variables, names, source, opening, body_end))
    return loops


# A package-level slice of structs: "var refusedMethods = []serveRefusal{ ... }". Its "name:" fields
# are the names a range over the variable hands to Register through a local alias.
STRUCT_LIST = re.compile(r"var\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\[\][A-Za-z_][A-Za-z0-9_.]*\s*\{(.*?)\n\}", re.S)


def _struct_list_names(text):
    """{variable: names} for every package-level []T{...} whose entries carry a name: field."""
    return {match.group(1): set(REFUSED_NAME.findall(match.group(2))) for match in STRUCT_LIST.finditer(text)}


def _parse_register_calls(text):
    """The method names a file registers, and the calls whose name the checker cannot read.

    Every `.Register(` call is read. Its first argument must be one string literal, or one identifier
    that a `range` loop in the same file binds to a map[string]... or []string literal. Anything else
    is an error: the author is told to use the literal form, never silently skipped.
    """
    tokens = _go_tokens(text)
    struct_names = _struct_list_names(text)
    loops = _range_loops(tokens)
    aliases = {}
    for variables, collection, source, body_start, body_end in loops:
        refused_collection = collection is None and source in struct_names
        if refused_collection:
            collection = struct_names[source]
        if collection is None:
            continue
        # name := r.name inside the loop: the local carries the list's names too.
        position = body_start + 1
        while position + 5 <= body_end:
            if (
                tokens[position][0] == "ident"
                and tokens[position + 1][1] == ":"
                and tokens[position + 2][1] == "="
                and tokens[position + 3][0] == "ident"
                and tokens[position + 3][1] in variables
                and tokens[position + 4][1] == "."
                and tokens[position + 5][0] == "ident"
                and tokens[position + 5][1] == "name"
            ):
                aliases[tokens[position][1]] = (collection, refused_collection)
            position += 1
    names = set()
    refused_names = set()
    problems = []
    for index, (kind, value, line) in enumerate(tokens):
        if (
            kind != "ident"
            or value != "Register"
            or index < 1
            or tokens[index - 1][0] != "punct"
            or tokens[index - 1][1] != "."
            or index + 1 >= len(tokens)
            or tokens[index + 1][0] != "punct"
            or tokens[index + 1][1] != "("
        ):
            continue
        position = index + 2
        depth = 0
        while position < len(tokens):
            kind, value, _ = tokens[position]
            if kind == "punct":
                if value in "([{":
                    depth += 1
                elif value in ")]}":
                    if depth == 0:
                        break
                    depth -= 1
                elif value == "," and depth == 0:
                    break
            position += 1
        argument = tokens[index + 2:position]
        if len(argument) == 1 and argument[0][0] == "string":
            names.add(argument[0][1])
            continue
        if len(argument) == 1 and argument[0][0] == "ident":
            variable = argument[0][1]
            resolved = False
            for variables, collection, source, body_start, body_end in loops:
                if variable in variables and body_start <= index <= body_end:
                    refused_collection = collection is None and source in struct_names
                    if refused_collection:
                        collection = struct_names[source]
                    if collection is not None:
                        (refused_names if refused_collection else names).update(collection)
                        resolved = True
                        break
            if not resolved and variable in aliases:
                collection, refused_collection = aliases[variable]
                (refused_names if refused_collection else names).update(collection)
                resolved = True
            if resolved:
                continue
        problems.append(
            (
                line,
                "cannot read the method name at this Register call: its first argument is not a string "
                'literal. Use Register("name", ...), or a map[string]... or []string literal that the same '
                "range walks and registers by its loop variable",
            )
        )
    return names, refused_names, problems


def parse_method_registry(texts, problems=None):
    """Return (implemented methods, refused methods) parsed from internal/forever/world's Go sources.

    texts is a list of file contents, or of (name, content) pairs. Every `.Register(` call is read; one
    whose first argument is not a readable literal is a problem (appended to `problems` as
    (name, line, message) when a list is given), not a silent omission. The older notPortedMethods
    slice is read too, so a tree that still uses it is not silently empty.
    """
    implemented = set()
    refused = set()
    for item in texts:
        name, text = item if isinstance(item, tuple) else ("", item)
        found, registered_refused, unreadable = _parse_register_calls(text)
        implemented.update(found)
        refused.update(registered_refused)
        if problems is not None:
            for line, message in unreadable:
                problems.append((name, line, message))
        for block in REFUSED_LIST.findall(text):
            refused.update(REFUSED_NAME.findall(block))
        for block in NOT_PORTED_LIST.findall(text):
            refused.update(_quoted_names(block))
    return implemented, refused


def _quoted_names(block):
    return re.findall(r'"([^"\n]+)"', block)


def load_method_registry(root):
    """Read internal/forever/world's registration, or (None, []) when that tree is not under root."""
    directory = os.path.join(root, "internal", "forever", "world")
    try:
        names = sorted(os.listdir(directory))
    except OSError:
        return None, []
    texts = []
    for name in names:
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        try:
            with open(os.path.join(directory, name), encoding="utf-8") as handle:
                texts.append((posixpath.join("internal", "forever", "world", name), handle.read()))
        except OSError:
            return None, []
    if not texts:
        return None, []
    problems = []
    registry = parse_method_registry(texts, problems)
    return registry, problems


def _code_lines(text):
    """Yield (line number, line) for fenced code (``` and ~~~) and indented code blocks."""
    fence = None
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        match = FENCE.match(stripped)
        if fence is None:
            if match:
                fence = (match.group(1)[0], len(match.group(1)))
                continue
            if line.startswith("\t"):
                yield number, line[1:]
            elif line.startswith("    "):
                yield number, line[4:]
            continue
        if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= fence[1]:
            fence = None
            continue
        yield number, line


def _check_commands(rel, text, registry, report):
    top, forever = registry
    for number, line in _code_lines(text):
        stripped = line.strip()
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


def _note_command_line(line, top, forever):
    """Record the commands one command line names (`forever-agent <word> [forever <word>]`)."""
    match = COMMAND_LINE.match(line.strip())
    if not match:
        return
    words = (match.group("rest") or "").split()
    if not words or not COMMAND_WORD.match(words[0]):
        return
    top.add(words[0])
    if words[0] == "forever" and len(words) >= 2 and COMMAND_WORD.match(words[1]):
        forever.add(words[1])


def _note_command_form(phrase, top, forever, bare=False):
    """Record a heading or table cell that names a command form.

    A bare command word counts only when bare is true: a heading, or a table cell under a header that
    names a command column. In any other table cell only "forever-agent <word>" or "forever <word>"
    names a command.
    """
    phrase = phrase.strip()
    match = COMMAND_FORM.match(phrase)
    if match:
        word = match.group(1)
        if phrase.startswith("forever ") and not phrase.startswith("forever-agent"):
            forever.add(word)
        elif phrase.startswith("forever-agent forever "):
            forever.add(word)
        else:
            top.add(word)
        return
    if bare and re.fullmatch(r"[a-z][a-z0-9-]*", phrase):
        top.add(phrase)


def _table_cells(line):
    """The stripped cells of one markdown table row."""
    return [cell.strip() for cell in line.strip().strip("|").split("|")]


def _table_separator(cells):
    """True for the |---|---| row under a table's header."""
    filled = [cell for cell in cells if cell]
    return bool(filled) and all(re.fullmatch(r":?-{3,}:?", cell) for cell in filled)


def _command_column(cell):
    """True when a table header cell names a command column ("Command", "Turtle-only command")."""
    text = re.sub(r"[^a-z]", "", cell.strip().strip("`").strip().lower())
    return "command" in text


def command_mentions(text):
    """Return (top-level, forever-subcommand) command names one page documents.

    A command is documented only as a command: a line in a fenced or indented code block that starts
    with forever-agent (`forever-agent <word>`, `forever-agent forever <word>`), a heading that names
    that form or is the command word itself, or a table cell that names the form. A bare backticked
    word counts only in a heading or under a table header that names a command column; in prose or any
    other table cell it is not documentation.
    """
    top, forever = set(), set()
    for _, line in _code_lines(text):
        _note_command_line(line, top, forever)
    in_table = False
    command_columns = set()
    for line in text.splitlines():
        heading = MD_HEADING.match(line)
        if heading:
            in_table = False
            _note_command_form(heading.group(1).strip().strip("`").strip(), top, forever, bare=True)
            continue
        if not line.lstrip().startswith("|"):
            in_table = False
            continue
        cells = _table_cells(line)
        if not in_table:
            in_table = True
            command_columns = {index for index, cell in enumerate(cells) if _command_column(cell)}
            continue
        if _table_separator(cells):
            continue
        for index, cell in enumerate(cells):
            for span in BACKTICK.findall(cell):
                _note_command_form(span.strip(), top, forever, bare=index in command_columns)
    return top, forever


def _read_allowlist(root, rel, report):
    """Read one "name # reason" per line; return {name: line number}. A line without a reason is a problem."""
    path = os.path.join(root, rel)
    try:
        with open(path, encoding="utf-8") as handle:
            lines = handle.read().splitlines()
    except OSError:
        return {}
    entries = {}
    for number, line in enumerate(lines, start=1):
        text = line.strip()
        if not text or text.startswith("#"):
            continue
        name, sep, reason = text.partition("#")
        name = name.strip()
        if not name:
            report(rel, number, "allow-list line has no name")
            continue
        if not sep or not reason.strip():
            report(rel, number, "allow-list entry %r has no reason after #" % name)
            continue
        if " " in name:
            report(rel, number, "allow-list entry %r is not one name" % name)
            continue
        entries[name] = number
    return entries


def _bundle_pages(root):
    bundle = os.path.join(root, BUNDLE)
    for directory, dirs, files in os.walk(bundle):
        dirs[:] = sorted(d for d in dirs if not d.startswith("."))
        for name in sorted(files):
            if name.endswith(".md"):
                full = os.path.join(directory, name)
                with open(full, encoding="utf-8") as handle:
                    yield os.path.join(*os.path.relpath(full, root).split(os.sep)), handle.read()


def _check_command_coverage(root, registry, report):
    """Every registered command is mentioned in a page, and the allow-list names registered commands only."""
    top, forever = registry
    mentioned_top, mentioned_forever = set(), set()
    for _, text in _bundle_pages(root):
        found_top, found_forever = command_mentions(text)
        mentioned_top |= found_top
        mentioned_forever |= found_forever
    allowed = _read_allowlist(root, UNDOCUMENTED_COMMANDS, report)
    for command in sorted(top - mentioned_top - set(allowed)):
        report(
            posixpath.join(BUNDLE, "index.md"),
            0,
            "registered command %r is in no manual page (as `forever-agent %s` or in backticks); document it, or "
            "add it to %s with a reason" % (command, command, UNDOCUMENTED_COMMANDS),
        )
    for command in sorted(forever - mentioned_forever - set(allowed)):
        report(
            posixpath.join(BUNDLE, "index.md"),
            0,
            "registered forever subcommand %r is in no manual page (as `forever %s` or a command line); document it, "
            "or add it to %s with a reason" % (command, command, UNDOCUMENTED_COMMANDS),
        )
    for name, number in sorted(allowed.items()):
        if name not in top and name not in forever:
            report(
                UNDOCUMENTED_COMMANDS,
                number,
                "allow-list entry %r is not a registered command (the list cannot rot)" % name,
            )


def _method_index_names(text):
    """The method names of an index page's table rows (the first column, in backticks)."""
    names = set()
    for line in text.splitlines():
        match = METHOD_ROW.match(line)
        if match:
            names.add(match.group(1))
    return names


def _not_ported_names(text):
    """The method names of the names table's first column (the other columns name replacements, not old methods)."""
    names = set()
    for line in text.splitlines():
        if not line.startswith("|"):
            continue
        first = line.split("|")[1]
        names.update(re.findall(r"`([A-Za-z0-9_]+)`", first))
    return names


def _read_page(root, rel):
    try:
        with open(os.path.join(root, rel), encoding="utf-8") as handle:
            return handle.read()
    except OSError:
        return None


def _check_method_coverage(root, registry, report):
    """The method index and the refused-methods page agree with the serve's method table."""
    implemented, refused = registry
    index_rel = posixpath.join(BUNDLE, "daemon", "method-index.md")
    index_text = _read_page(root, index_rel)
    if index_text is None:
        report(index_rel, 0, "the method index page is missing")
        return
    documented = _method_index_names(index_text)
    allowed = _read_allowlist(root, UNDOCUMENTED_METHODS, report)
    for method in sorted(implemented - documented - set(allowed)):
        report(
            index_rel,
            0,
            "registered daemon method %r has no row in this page; document it, or add it to %s with a reason"
            % (method, UNDOCUMENTED_METHODS),
        )
    for method in sorted(documented - implemented):
        report(index_rel, 0, "method %r is not registered by the serve (the page cannot outlive the code)" % method)
    for name, number in sorted(allowed.items()):
        if name not in implemented and name not in refused:
            report(
                UNDOCUMENTED_METHODS,
                number,
                "allow-list entry %r is not a registered daemon method (the list cannot rot)" % name,
            )

    refused_rel = posixpath.join(BUNDLE, "daemon", "not-ported-methods.md")
    refused_text = _read_page(root, refused_rel)
    if refused_text is None:
        report(refused_rel, 0, "the not-ported methods page is missing")
        return
    listed = _not_ported_names(refused_text)
    for method in sorted(refused - listed):
        report(refused_rel, 0, "refused method %r is named in no row of this page" % method)
    for method in sorted(listed - refused):
        report(refused_rel, 0, "method %r is not in refusedMethods any more (the page cannot outlive the code)" % method)


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

    entry = re.compile(r"^\s*[*+-]\s+\[[^\]]*\]\(\s*([^)\s]+)\s*\)\s*[-–—]\s*(.+?)\s*$")
    for number, line in enumerate(text.splitlines(), start=1):
        match = entry.match(line)
        if not match:
            continue
        resolved = _resolve(rel, match.group(1))
        if resolved is None or not resolved.endswith(".md"):
            continue
        if posixpath.basename(resolved) in RESERVED:
            continue
        page_text = _read_page(root, resolved)
        if page_text is None:
            continue
        try:
            data, _ = parse_frontmatter(page_text)
        except FrontmatterError:
            continue
        page_description = data.get("description")
        if not _nonempty(page_description):
            continue
        if page_description.strip() != match.group(2).strip():
            report(
                rel,
                number,
                "index description does not match %s: %r != %r"
                % (resolved, match.group(2).strip(), page_description.strip()),
            )

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
    method_registry, method_problems = load_method_registry(root)
    for rel, line, message in method_problems:
        report(rel, line, message)
    for directory, dirs, files in os.walk(bundle):
        dirs[:] = sorted(d for d in dirs if not d.startswith("."))
        dirs.sort()
        names = set(files)
        rel_dir = posixpath.join(*os.path.relpath(directory, root).split(os.sep))
        if "index.md" not in names:
            report(posixpath.join(rel_dir, "index.md"), 0, "directory has no index.md")
        for name in sorted(files):
            if name.startswith("."):
                # The coverage allow-lists: plain text, not pages, and their entries are checked once below.
                continue
            full = os.path.join(directory, name)
            rel = posixpath.join(*os.path.relpath(full, root).split(os.sep))
            if not name.endswith(".md"):
                report(rel, 0, "not a Markdown file: the bundle holds only .md pages (assets live outside it)")
                with open(full, encoding="utf-8", errors="replace", newline="") as handle:
                    text = handle.read()
                _check_privacy(rel, text, report)
                continue
            with open(full, encoding="utf-8", newline="") as handle:
                text = handle.read()
            if "\r\n" in text or "\r" in text:
                report(rel, 1, "CRLF line endings: every page is written with LF")
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
                    _check_concept(rel, data, root, report)

            _check_links(rel, text, root, report)
            _check_privacy(rel, text, report)
            if registry is not None:
                _check_commands(rel, text, registry, report)

    if registry is not None:
        _check_command_coverage(root, registry, report)
    if method_registry is not None:
        _check_method_coverage(root, method_registry, report)
    _check_docs_tree_privacy(root, report)

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
