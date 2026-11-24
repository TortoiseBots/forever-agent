#!/usr/bin/env python3
"""Decide whether a pull request keeps the docs in step with the code.

DOCS-SYNC.md is the rule: a change a user can see updates docs/manual/ in the same pull request. This script is the
part a machine can check: a pull request that changes the CLI (cmd/forever-agent/), a serve method file
(internal/forever/world/serve*_methods.go), adds a flag or registers a method has to change something under
docs/manual/ or README.md, or say in its body why no doc was needed.

Usage:
    python3 tools/verify_docs_sync.py --changed-files FILE --diff FILE

--changed-files is one path per line (git diff --name-only BASE...HEAD). --diff is the unified diff of the same range;
only its added lines are read, for the flag heuristic. The pull request body comes from the PR_BODY environment
variable, never from a command line: a body with shell syntax is only text.

The decision itself (decide) is a pure function of the changed paths, the added lines and the body, so its cases are
unit-tested in tools/test_verify_docs_sync.py.

The rule, in the checker's terms:
  * a trigger is a changed path under cmd/forever-agent/, a changed internal/forever/world/serve*_methods.go, or an
    added line that calls a flag constructor on any FlagSet variable (fs.String, flags.Bool, flag.Duration,
    fs.Var, ...) or any Register call;
  * a trigger passes when the pull request changes a path under docs/manual/ or README.md;
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

# The paths whose change means a user can see something new: the CLI, and the serve's method registration files.
CLI_PREFIX = "cmd/forever-agent/"
METHOD_PREFIX = "internal/forever/world/serve"
METHOD_SUFFIX = "_methods.go"

# A changed path that satisfies the rule.
DOC_PREFIX = "docs/manual/"
DOC_FILE = "README.md"

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
    "this pull request changes what a user sees (%s) and changes nothing under %s or %s. "
    "Update the manual page the change belongs to (the table in %s says which page), or put a line "
    "\"%s, because <reason>\" in the pull request body when the change has no user-visible effect."
)


def _is_doc(path):
    return path == DOC_FILE or path.startswith(DOC_PREFIX)


def _is_trigger_path(path):
    if path.startswith(CLI_PREFIX):
        return True
    name = path.rsplit("/", 1)[-1]
    return path.startswith(METHOD_PREFIX) and name.endswith(METHOD_SUFFIX)


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


def decide(changed_paths, diff_lines, body):
    """Return the problems a pull request has (empty means it may merge).

    changed_paths: every path the pull request changes.
    diff_lines: the lines the diff adds (see added_lines).
    body: the pull request body.
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
    flag_lines = [line.strip() for line in diff_lines if _is_flag_or_register_line(line)]
    if flag_lines:
        triggers.append("a new flag or method registration (%s)" % flag_lines[0][:120])
    if not triggers:
        return []
    if any(_is_doc(path) for path in changed_paths):
        return []
    if _waived(body):
        return []
    return [MESSAGE % (", ".join(triggers), DOC_PREFIX, DOC_FILE, RULE_FILE, WAIVER)]


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
    problems = decide(changed, added_lines(diff), os.environ.get("PR_BODY", ""))
    for problem in problems:
        print("docs-sync: %s" % problem)
    if problems:
        return 1
    print("docs-sync: ok (%d changed file(s))" % len(changed))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
