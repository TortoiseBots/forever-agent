#!/usr/bin/env python3
"""Automatic labels for issues and pull requests (docs/CI.md, "Automatic labels").

Default mode is what .github/workflows/label-issues.yml runs: it labels ONE issue from the title and body the workflow
passes in the environment, never through the shell (the same text is only data here):

  ISSUE_NUMBER=12 ISSUE_TITLE=... ISSUE_BODY=... GH_TOKEN=... python3 -I tools/label_issue.py

RULES below is the keyword table: each entry is a label, a regular expression matched case-insensitively on whole
words, and the scope it reads. A "title" rule reads the title only: its word is too common in a body (a list of tickets
can say "raids" once, or a settings file's field list can say "plans") to mean the issue is about that area. A "both"
rule reads "title\nbody". Two title rules follow: a bug word adds `bug`, otherwise a title starting "Agent:" or
"[agent]" adds `enhancement`. The script only ADDS labels from its own tables in its default (workflow) mode; it never
removes one there, and the people-owned labels (the priority labels, TortoiseBots) are not among them.

--backfill labels every OPEN issue (title and body) and OPEN pull request (changed files) that does not have these
labels yet:

  python3 -I tools/label_issue.py --backfill --dry-run
  python3 -I tools/label_issue.py --backfill

--prune, with --backfill, is the one-time clean-up after a rule was narrowed: an OPEN issue also loses each label the
automation can apply that the current rules no longer give. It only removes from the automation's own label set below
and never a label a rule still gives; the people-owned labels are outside that set. A label carries no record of who
added it, so a `bug`, `enhancement` or keyword label a person set by hand and no rule gives is removed too, and the run
says so first. The workflow never uses it.

  python3 -I tools/label_issue.py --backfill --prune --dry-run
  python3 -I tools/label_issue.py --backfill --prune

Issues use RULES on each rule's scope. Pull requests use their changed files against the path rules of
.github/labeler.yml, which is parsed here, so the path table exists in one file only. --dry-run prints one line per
change, "issue N: +a,+b", "issue N: -c" or "pr N: +a,+b", and changes nothing.
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import quote

# (label, alternatives, scope). Matched on whole words, case-insensitive: \b keeps "braid" out of `raid` and "emails"
# out of `mail`. Plural and hyphen variants where the prose uses them. "title" reads the title only: those words appear
# in bodies in lists and quotations that do not make the issue about that area; "both" reads "title\nbody".
RULES = (
    ("dungeon", r"dungeons?|dungeon-play|dungeon-watch|dungeon run|ragefire|wailing", "title"),
    ("raid", r"raids?", "title"),
    ("battleground", r"battlegrounds?|bg-watch", "title"),
    ("plans", r"dungeon plans?|plan files?|plan-check|plan-probe|plan tools|route tools|navmesh", "both"),
    ("daemon", r"daemons?", "both"),
    ("dashboard", r"dashboards?", "title"),
    ("guild", r"guilds?", "title"),
    ("addon", r"add-?ons?|tbm", "both"),
    ("testing", r"bench|benchmarks?|trials?|spell-score|fight-score|bug-hunter", "both"),
    ("player-actions", r"bank|mail|auctions?|trade|vendors?|quests?|loot|trainers?|taxi", "both"),
    ("live-check", r"live (?:sessions?|checks?|runs?)", "both"),
    ("parity", r"parity", "both"),
    ("documentation", r"docs|documentation|readme|manuals?", "title"),
)

# Every label the automation can apply, and so the only set --prune may remove from. All of them are in
# .github/labels.yml; the people-owned labels are not (the tests check both).
AUTOMATION_LABELS = frozenset(label for label, _, _ in RULES) | {"bug", "enhancement"}

# A label carries no record of who added it, so --prune cannot tell one this script added from one a person set by
# hand: a `bug`, `enhancement` or keyword label no rule still gives goes too. Both prune runs print this first.
PRUNE_NOTE = ("prune: a label does not record who added it, so a hand-set bug, enhancement or keyword label that no "
              "rule gives is removed too")

# Title only. A whole word from here is a bug report; the repo's titles say "fix", "crash", "broken" and so on.
BUG_WORDS = re.compile(r"\b(?:fix(?:es|ed)?|fails?|broken|refused|crash(?:es|ed)?)\b", re.IGNORECASE)
# "otherwise a title starting Agent: or [agent] with no bug word -> enhancement": the agent writes those titles.
AGENT_TITLE = re.compile(r"^\s*(?:agent:|\[agent\])", re.IGNORECASE)

COMPILED_RULES = tuple((label, re.compile(r"\b(?:" + pattern + r")\b", re.IGNORECASE), scope)
                       for label, pattern, scope in RULES)

ROOT = Path(__file__).resolve().parent.parent
LABELER_FILE = ROOT / ".github" / "labeler.yml"


def labels_for_text(title, body):
    """The labels RULES and the title rules give this issue text, in table order, each once."""
    title = title or ""
    both = title + "\n" + (body or "")
    labels = []
    for label, pattern, scope in COMPILED_RULES:
        if pattern.search(title if scope == "title" else both) and label not in labels:
            labels.append(label)
    if BUG_WORDS.search(title):
        if "bug" not in labels:
            labels.append("bug")
    elif AGENT_TITLE.match(title) and "enhancement" not in labels:
        labels.append("enhancement")
    return labels


def _unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def _inline_list(value):
    """`["^fix/", 'x']` -> ["^fix/", "x"]; a bare scalar -> [scalar]."""
    value = value.strip()
    if not value:
        return []
    if value.startswith("[") and value.endswith("]"):
        inner = value[1:-1].strip()
        if not inner:
            return []
        return [_unquote(item) for item in inner.split(",") if item.strip()]
    return [_unquote(value)]


# The labeler.yml shape this file understands: one label per top-level key, matchers as list items, and the keys below.
# `any:`/`all:` only choose between matchers, which the backfill does not need: it applies the path globs and reads the
# branch regexes for the tests.
_KEY = re.compile(
    r"^(any|all|changed-files|head-branch|base-branch|any-glob-to-any-file|any-glob-to-all-files|"
    r"all-globs-to-any-file|all-globs-to-all-files):\s*(.*)$"
)


def parse_labeler(text):
    """labeler.yml -> {label: {"globs": [...], "branches": [...]}} in file order."""
    rules = {}
    label = None
    section = None
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip())
        stripped = line.strip()
        if indent == 0:
            if not stripped.endswith(":"):
                raise ValueError("labeler.yml: expected a label key, got %r" % raw)
            label = _unquote(stripped[:-1].strip())
            if label in rules:
                raise ValueError("labeler.yml: label %r appears twice" % label)
            rules[label] = {"globs": [], "branches": []}
            section = None
            continue
        if label is None:
            raise ValueError("labeler.yml: a matcher before the first label: %r" % raw)
        item = stripped[2:].strip() if stripped.startswith("- ") else stripped
        match = _KEY.match(item)
        if match:
            key, value = match.group(1), _inline_list(match.group(2))
            if key == "head-branch":
                section = "branches"
                rules[label]["branches"].extend(value)
            elif key in ("any-glob-to-any-file", "all-globs-to-any-file", "any-glob-to-all-files"):
                section = "globs"
                rules[label]["globs"].extend(value)
            elif key in ("all-globs-to-all-files",):
                section = "globs"  # every glob must match; the backfill treats it as one more path rule
                rules[label]["globs"].extend(value)
            else:
                section = None
            continue
        if not stripped.startswith("- "):
            raise ValueError("labeler.yml: expected a list item, got %r" % raw)
        if section is None:
            raise ValueError("labeler.yml: a value outside head-branch/any-glob-to-any-file: %r" % raw)
        rules[label][section].append(_unquote(item))
    return rules


def glob_regex(pattern):
    """A minimatch pattern as an anchored regular expression: `*` stays inside one path segment, `**/` spans zero or
    more directories, `?` is one character, `[...]` is a character class."""
    out = []
    i = 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            out.append(r"(?:[^/]+/)*")
            i += 3
        elif pattern.startswith("**", i):
            out.append(r".*")
            i += 2
        elif pattern[i] == "*":
            out.append(r"[^/]*")
            i += 1
        elif pattern[i] == "?":
            out.append(r"[^/]")
            i += 1
        elif pattern[i] == "[":
            end = pattern.find("]", i + 1)
            if end == -1:
                out.append(re.escape("["))
                i += 1
            else:
                cls = pattern[i:end + 1]
                if cls.startswith("[!"):
                    cls = "[^" + cls[2:]
                out.append(cls)
                i = end + 1
        else:
            out.append(re.escape(pattern[i]))
            i += 1
    return re.compile("^" + "".join(out) + "$")


def labels_for_files(rules, paths):
    """The labels whose changed-file globs match at least one of `paths`, in table order."""
    labels = []
    for label, rule in rules.items():
        patterns = [glob_regex(p) for p in rule["globs"]]
        if any(pattern.match(path) for pattern in patterns for path in paths):
            labels.append(label)
    return labels


def run_gh(args):
    """`gh <args>`, stdout returned; a non-zero exit raises with gh's stderr."""
    proc = subprocess.run(["gh", *args], text=True, capture_output=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        raise SystemExit("gh %s failed (%d)" % (" ".join(args[:3]), proc.returncode))
    return proc.stdout


def gh_json(args, runner=run_gh):
    out = runner(args).strip()
    return json.loads(out) if out else None


def default_repo(runner=run_gh):
    repo = os.environ.get("REPO", "").strip()
    if repo:
        return repo
    return gh_json(["repo", "view", "--json", "nameWithOwner"], runner)["nameWithOwner"]


def _existing_names(item):
    return {label["name"] for label in item.get("labels") or []}


def _ordered_names(item):
    return [label["name"] for label in item.get("labels") or []]


def _apply(kind, number, labels, repo, dry_run, runner):
    line = "%s %s: +%s" % (kind, number, ",".join(labels))
    print(line)
    if dry_run:
        return
    # The REST labels endpoint, not `gh issue edit`/`gh pr edit`: it applies to issues and pull requests alike, and
    # `gh pr edit` (gh 2.45) asks GraphQL for project cards, which GitHub now refuses on every request.
    args = ["api", "--method", "POST", "repos/%s/issues/%s/labels" % (repo, number)]
    for label in labels:
        args += ["-f", "labels[]=" + label]
    runner(args)


def _remove(kind, number, labels, repo, dry_run, runner):
    print("%s %s: -%s" % (kind, number, ",-".join(labels)))
    if dry_run:
        return
    for label in labels:
        runner(["api", "--method", "DELETE",
                "repos/%s/issues/%s/labels/%s" % (repo, number, quote(label, safe=""))])


def label_env_issue(repo, dry_run, runner=run_gh):
    number = os.environ.get("ISSUE_NUMBER", "").strip()
    if not number.isdigit():
        raise SystemExit("ISSUE_NUMBER is not set (this mode runs from the label-issues workflow)")
    title = os.environ.get("ISSUE_TITLE", "")
    body = os.environ.get("ISSUE_BODY", "")
    labels = labels_for_text(title, body)
    if not labels:
        print("issue %s: no label rule matched" % number)
        return 0
    _apply("issue", number, labels, repo, dry_run, runner)
    return len(labels)


def backfill(repo, limit, dry_run, runner=run_gh, prune=False):
    """Label every open issue and pull request missing these labels; returns (items, added, removed).

    With `prune`, an issue also loses every label of AUTOMATION_LABELS that the current rules no longer give; a label a
    rule still gives, and every label outside that set (the people-owned ones), is left alone. A label records who
    added it nowhere, so a person's own `bug`, `enhancement` or keyword label goes too (PRUNE_NOTE).
    """
    rules = parse_labeler(LABELER_FILE.read_text(encoding="utf8"))
    if prune:
        print(PRUNE_NOTE)
    items = 0
    added = 0
    removed = 0
    issues = gh_json(["issue", "list", "--repo", repo, "--state", "open", "--limit", str(limit),
                      "--json", "number,title,body,labels"], runner) or []
    for issue in issues:
        wanted = labels_for_text(issue.get("title"), issue.get("body"))
        current = _existing_names(issue)
        missing = [label for label in wanted if label not in current]
        stale = []
        if prune:
            stale = [label for label in _ordered_names(issue) if label in AUTOMATION_LABELS and label not in wanted]
        if missing or stale:
            items += 1
        if stale:
            _remove("issue", issue["number"], stale, repo, dry_run, runner)
            removed += len(stale)
        if missing:
            _apply("issue", issue["number"], missing, repo, dry_run, runner)
            added += len(missing)
    pulls = gh_json(["pr", "list", "--repo", repo, "--state", "open", "--limit", str(limit),
                     "--json", "number"], runner) or []
    for pull in pulls:
        detail = gh_json(["pr", "view", str(pull["number"]), "--repo", repo, "--json", "files,labels"],
                         runner) or {}
        paths = [f["path"] for f in detail.get("files") or []]
        wanted = labels_for_files(rules, paths)
        missing = [label for label in wanted if label not in _existing_names(detail)]
        if not missing:
            continue
        _apply("pr", pull["number"], missing, repo, dry_run, runner)
        items += 1
        added += len(missing)
    mode = "dry-run" if dry_run else "applied"
    print("%s: %d issues and pull requests, %d labels added, %d removed" % (mode, items, added, removed))
    return items, added, removed


def main(argv=None):
    parser = argparse.ArgumentParser(description="Label issues by keywords and pull requests by changed files.")
    parser.add_argument("--backfill", action="store_true",
                        help="label every open issue and pull request missing these labels")
    parser.add_argument("--prune", action="store_true",
                        help="with --backfill: also remove the automation labels an issue's current rules no longer give")
    parser.add_argument("--dry-run", action="store_true", help="print the labels, change nothing")
    parser.add_argument("--repo", default=None, help="OWNER/REPO (default: $REPO, else the current repository)")
    parser.add_argument("--limit", type=int, default=500, help="how many open issues/pull requests to read")
    args = parser.parse_args(argv)
    if args.prune and not args.backfill:
        parser.error("--prune only works with --backfill")
    repo = args.repo or default_repo()
    if args.backfill:
        backfill(repo, args.limit, args.dry_run, prune=args.prune)
    else:
        label_env_issue(repo, args.dry_run)
    return 0


if __name__ == "__main__":
    sys.exit(main())
