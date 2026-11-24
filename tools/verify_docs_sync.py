#!/usr/bin/env python3
"""Decide whether a pull request keeps the docs in step with the code.

DOCS-SYNC.md is the rule: a change a user can see updates docs/manual/ in the same pull request. This script is the
part a machine can check: a pull request that changes the CLI (cmd/forever-agent/), a serve or session source
(internal/forever/world/serve*.go, *_session.go), adds a flag or registers (or removes) a method has to change a
concept page under docs/manual/, or say in its body why no doc was needed. README.md carries some of the same text
but does not satisfy the rule on its own.

Usage:
    python3 tools/verify_docs_sync.py --changed-files FILE --diff FILE

--changed-files is one path per line (git diff --name-only BASE...HEAD). --diff is the unified diff of the same range;
only its added lines are read, for the flag heuristic. The pull request body comes from the PR_BODY environment
variable, never from a command line: a body with shell syntax is only text.

The decision itself (decide) is a pure function of the changed paths, the added lines and the body, so its cases are
unit-tested in tools/test_verify_docs_sync.py.

The rule, in the checker's terms:
  * a trigger is a changed path under cmd/forever-agent/, a changed internal/forever/world/serve*.go or
    *_session.go (not a _test.go), or an added or removed line that calls a flag constructor on any FlagSet
    variable (fs.String, flags.Bool, flag.Duration, fs.Var, ...) or any Register call;
  * a trigger passes when the pull request changes a concept page under docs/manual/ (an index.md, log.md or
    README.md alone is bookkeeping and does not count);
  * it also passes when the body has, outside a fenced block, a line "Docs: not needed, because <reason>";
  * anything else fails, and the message names the trigger and points at DOCS-SYNC.md.

The checker cannot see a change to a plan field, a stop reason, an output field, an error text or a
format that no path or call in the diff marks. DOCS-SYNC.md says so; the author still has to check those.

Exit codes: 0 the change is fine (or has no trigger); 1 a trigger without a doc change; 2 a usage error or an
unreadable file.
"""

import argparse
import os
import re
import sys

# The paths whose change means a user can see something new: the CLI, and the serve and session
# sources (they register methods and hold method behaviour, not only the serve*_methods.go files).
CLI_PREFIX = "cmd/forever-agent/"
WORLD_PREFIX = "internal/forever/world/"

# A changed path that satisfies the rule: a concept page under docs/manual/. README.md carries the
# same text for some pages but does not satisfy the rule alone; the update log and the index files
# are bookkeeping and never do.
DOC_PREFIX = "docs/manual/"
BOOKKEEPING = ("docs/manual/log.md",)

# The flag package's constructors and Var-style registrations, on any FlagSet variable (fs, flags,
# flag, ...): an added line that calls one adds a flag. A registered daemon method (any Register call)
# is a trigger too, whatever file it is in.
FLAG_CALL = re.compile(
    r"\b[A-Za-z_][A-Za-z0-9_]*\.(?:String|StringVar|Bool|BoolVar|Int|IntVar|Int64|Int64Var|Uint|UintVar"
    r"|Uint64|Uint64Var|Float64|Float64Var|Duration|DurationVar|Var|Func)\s*\("
)
REGISTER_CALL = re.compile(r"\bRegister\s*\(")
# Any call in the flag package (NewFlagSet included); a new flag set is a new user-visible command.
FLAG_PACKAGE_CALL = re.compile(r"\bflag\.[A-Za-z_][A-Za-z0-9_]*\s*\(")

# The line that opts a pull request out, and the text every failure tells the author to read. The line
# must give a reason ("Docs: not needed, because ...") and must not sit inside a fenced block.
WAIVER = "Docs: not needed"
WAIVER_LINE = re.compile(r"^Docs:\s*not needed,\s*because\s+\S", re.IGNORECASE)
RULE_FILE = "DOCS-SYNC.md"

MESSAGE = (
    "this pull request changes what a user sees (%s) and changes no concept page under %s. "
    "Update the manual page the change belongs to (the table in %s says which page), or put a line "
    "\"%s, because <reason>\" in the pull request body when the change has no user-visible effect."
)


def _is_doc(path):
    if not path.startswith(DOC_PREFIX) or not path.endswith(".md"):
        return False
    if path in BOOKKEEPING or path.endswith("/index.md"):
        return False
    return True


def _is_trigger_path(path):
    if path.startswith(CLI_PREFIX):
        return True
    if not path.startswith(WORLD_PREFIX) or not path.endswith(".go") or path.endswith("_test.go"):
        return False
    name = path.rsplit("/", 1)[-1]
    return name.startswith("serve") or name.endswith("_session.go")


def _waived(body):
    """True when a line outside a fenced block is "Docs: not needed, because <text>"."""
    fence = None
    for line in body.splitlines():
        stripped = line.strip()
        match = re.match(r"^(`{3,}|~{3,})", stripped)
        if fence is not None:
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= fence[1]:
                fence = None
            continue
        if match:
            fence = (match.group(1)[0], len(match.group(1)))
            continue
        if WAIVER_LINE.match(stripped):
            return True
    return False


def added_lines(diff_text):
    """The lines a unified diff adds, without its +++ headers."""
    added = []
    for line in diff_text.splitlines():
        if line.startswith("+") and not line.startswith("+++"):
            added.append(line[1:])
    return added


def removed_lines(diff_text):
    """The lines a unified diff removes, without its --- headers."""
    removed = []
    for line in diff_text.splitlines():
        if line.startswith("-") and not line.startswith("---"):
            removed.append(line[1:])
    return removed


def decide(changed_paths, diff_lines, body, removed_lines=()):
    """Return the problems a pull request has (empty means it may merge).

    changed_paths: every path the pull request changes.
    diff_lines: the lines the diff adds (see added_lines).
    body: the pull request body.
    removed_lines: the lines the diff removes; a removed flag or registration is a trigger too.
    """
    triggers = []
    cli = sorted(path for path in changed_paths if path.startswith(CLI_PREFIX))
    methods = sorted(
        path for path in changed_paths if _is_trigger_path(path) and not path.startswith(CLI_PREFIX)
    )
    if cli:
        triggers.append("the CLI (%s)" % ", ".join(cli[:3]) + (", ..." if len(cli) > 3 else ""))
    if methods:
        triggers.append("the serve's method table (%s)" % ", ".join(methods))
    trigger_lines = list(diff_lines) + list(removed_lines)
    flag_lines = [line.strip() for line in trigger_lines if _is_flag_or_register_line(line)]
    if flag_lines:
        triggers.append("a new flag or method registration (%s)" % flag_lines[0][:120])
    if not triggers:
        return []
    if any(_is_doc(path) for path in changed_paths):
        return []
    if _waived(body):
        return []
    return [MESSAGE % (", ".join(triggers), DOC_PREFIX, RULE_FILE, WAIVER)]


def _is_flag_or_register_line(line):
    """True for an added line that calls a flag constructor or Register; a comment is not a call."""
    code = line.split("//", 1)[0]
    return bool(FLAG_CALL.search(code) or REGISTER_CALL.search(code) or FLAG_PACKAGE_CALL.search(code))


def _read(path):
    with open(path, encoding="utf-8", errors="replace") as handle:
        return handle.read()


def main(argv):
    parser = argparse.ArgumentParser(description="Check the docs-sync rule for one pull request.")
    parser.add_argument("--changed-files", required=True, help="a file with one changed path per line")
    parser.add_argument("--diff", required=True, help="the unified diff of the same range")
    args = parser.parse_args(argv[1:])
    try:
        changed = [line.strip() for line in _read(args.changed_files).splitlines() if line.strip()]
        diff = _read(args.diff)
    except OSError as error:
        print("verify_docs_sync: %v" % error, file=sys.stderr)
        return 2
    problems = decide(
        changed,
        added_lines(diff),
        os.environ.get("PR_BODY", ""),
        removed_lines(diff),
    )
    for problem in problems:
        print("docs-sync: %s" % problem)
    if problems:
        return 1
    print("docs-sync: ok (%d changed file(s))" % len(changed))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
