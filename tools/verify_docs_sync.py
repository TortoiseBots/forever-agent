#!/usr/bin/env python3
"""Decide whether a pull request keeps the docs in step with the code.

DOCS-SYNC.md is the rule: a change a user can see updates docs/manual/ in the same pull request. This script is the
part a machine can check: a pull request that changes the CLI (cmd/forever-agent/), a serve method file
(internal/forever/world/serve*_methods.go) or adds a flag has to change something under docs/manual/ or README.md, or
say in its body why no doc was needed.

Usage:
    python3 tools/verify_docs_sync.py --changed-files FILE --diff FILE

--changed-files is one path per line (git diff --name-only BASE...HEAD). --diff is the unified diff of the same range;
only its added lines are read, for the flag heuristic. The pull request body comes from the PR_BODY environment
variable, never from a command line: a body with shell syntax is only text.

The decision itself (decide) is a pure function of the changed paths, the added lines and the body, so its cases are
unit-tested in tools/test_verify_docs_sync.py.

The rule, in the checker's terms:
  * a trigger is a changed path under cmd/forever-agent/, a changed internal/forever/world/serve*_methods.go, or an
    added line that calls the flag package (fs.String, fs.Bool, fs.Duration, fs.Var, ...);
  * a trigger passes when the pull request changes a path under docs/manual/ or README.md;
  * it also passes when the body has a line starting "Docs: not needed" (the reason follows it);
  * anything else fails, and the message names the trigger and points at DOCS-SYNC.md.

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

# The flag package's constructors and Var-style registrations: an added line that calls one adds a flag.
FLAG_CALL = re.compile(
    r"\bfs\.(?:String|StringVar|Bool|BoolVar|Int|IntVar|Int64|Int64Var|Uint|UintVar|Uint64|Uint64Var"
    r"|Float64|Float64Var|Duration|DurationVar|Var|Func)\s*\("
)

# The line that opts a pull request out, and the text every failure tells the author to read.
WAIVER = "Docs: not needed"
RULE_FILE = "DOCS-SYNC.md"

MESSAGE = (
    "this pull request changes what a user sees (%s) and changes nothing under %s or %s. "
    "Update the manual page the change belongs to (the table in %s says which page), or put a line starting "
    "\"%s, because ...\" in the pull request body when the change has no user-visible effect."
)


def _is_doc(path):
    return path == DOC_FILE or path.startswith(DOC_PREFIX)


def _is_trigger_path(path):
    if path.startswith(CLI_PREFIX):
        return True
    name = path.rsplit("/", 1)[-1]
    return path.startswith(METHOD_PREFIX) and name.endswith(METHOD_SUFFIX)


def _waived(body):
    return any(line.strip().startswith(WAIVER) for line in body.splitlines())


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
    flag_lines = [line.strip() for line in diff_lines if FLAG_CALL.search(line)]
    if flag_lines:
        triggers.append("a new flag (%s)" % flag_lines[0][:120])
    if not triggers:
        return []
    if any(_is_doc(path) for path in changed_paths):
        return []
    if _waived(body):
        return []
    return [MESSAGE % (", ".join(triggers), DOC_PREFIX, DOC_FILE, RULE_FILE, WAIVER)]


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
