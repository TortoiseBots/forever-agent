#!/usr/bin/env python3
"""Creates and updates the labels .github/labels.yml lists (docs/CI.md, "Automatic labels").

  python3 -I tools/sync_labels.py --dry-run            # print what would change
  python3 -I tools/sync_labels.py                      # make it so
  python3 -I tools/sync_labels.py --repo OWNER/REPO

The script is idempotent: a label that does not exist is created, one whose colour or description differs is edited,
one that already matches is left alone. It NEVER deletes a label and NEVER renames one, and it reads only labels.yml,
so a label people own (the priority labels, TortoiseBots) is outside its reach by construction.

Needs the `gh` CLI and a token for the repository (GH_TOKEN or gh's own login).
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LABELS_FILE = ROOT / ".github" / "labels.yml"
COLOR_RE = re.compile(r"^[0-9a-fA-F]{6}$")


def _unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def parse_labels(text):
    """labels.yml -> [{"name", "color", "description"}] in file order; the documented block shape only."""
    labels = []
    for lineno, raw in enumerate(text.splitlines(), 1):
        line = "" if raw.lstrip().startswith("#") else raw.split("#", 1)[0].rstrip()
        stripped = line.strip()
        if not stripped:
            continue
        if stripped.startswith("- name:"):
            labels.append({"name": _unquote(stripped.split(":", 1)[1]), "color": "", "description": ""})
            if not labels[-1]["name"]:
                raise ValueError("labels.yml line %d: empty name" % lineno)
        elif stripped.startswith("color:"):
            if not labels:
                raise ValueError("labels.yml line %d: color before the first name" % lineno)
            labels[-1]["color"] = _unquote(stripped.split(":", 1)[1])
        elif stripped.startswith("description:"):
            if not labels:
                raise ValueError("labels.yml line %d: description before the first name" % lineno)
            labels[-1]["description"] = _unquote(stripped.split(":", 1)[1])
        else:
            raise ValueError("labels.yml line %d: expected '- name:', 'color:' or 'description:', got %r" % (lineno, raw))
    seen = set()
    for label in labels:
        if label["name"] in seen:
            raise ValueError("labels.yml: label %r appears twice" % label["name"])
        seen.add(label["name"])
        if not COLOR_RE.match(label["color"]):
            raise ValueError("labels.yml: %r needs a 6-digit hex color, got %r" % (label["name"], label["color"]))
        if len(label["description"]) > 100:
            raise ValueError("labels.yml: %r has a %d-character description; GitHub's limit is 100"
                             % (label["name"], len(label["description"])))
    return labels


def run_gh(args):
    proc = subprocess.run(["gh", *args], text=True, capture_output=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        raise SystemExit("gh %s failed (%d)" % (" ".join(args[:2]), proc.returncode))
    return proc.stdout


def labels_in_repo(repo, runner=run_gh):
    out = runner(["label", "list", "--repo", repo, "--limit", "500", "--json", "name,color,description"])
    return {label["name"]: label for label in json.loads(out or "[]")}


def default_repo(runner=run_gh):
    repo = os.environ.get("REPO", "").strip()
    if repo:
        return repo
    return json.loads(runner(["repo", "view", "--json", "nameWithOwner"]))["nameWithOwner"]


def sync(labels, repo, dry_run, runner=run_gh):
    """Create or update each label; returns (actions, created, updated) where actions are printable lines."""
    existing = labels_in_repo(repo, runner)
    actions = []
    created = 0
    updated = 0
    for label in labels:
        name = label["name"]
        current = existing.get(name)
        if current is None:
            actions.append("create label %s (%s)" % (name, label["description"]))
            created += 1
            if not dry_run:
                runner(["label", "create", name, "--repo", repo, "--color", label["color"],
                        "--description", label["description"]])
        elif (current.get("color") or "").lstrip("#").lower() != label["color"].lower() \
                or (current.get("description") or "") != label["description"]:
            actions.append("update label %s (%s)" % (name, label["description"]))
            updated += 1
            if not dry_run:
                runner(["label", "edit", name, "--repo", repo, "--color", label["color"],
                        "--description", label["description"]])
    return actions, created, updated


def main(argv=None):
    parser = argparse.ArgumentParser(description="Create and update the labels .github/labels.yml lists.")
    parser.add_argument("--repo", default=None, help="OWNER/REPO (default: $REPO, else the current repository)")
    parser.add_argument("--file", default=str(LABELS_FILE), help="labels file (default: .github/labels.yml)")
    parser.add_argument("--dry-run", action="store_true", help="print the changes, make none")
    args = parser.parse_args(argv)
    labels = parse_labels(Path(args.file).read_text(encoding="utf8"))
    repo = args.repo or default_repo()
    actions, created, updated = sync(labels, repo, args.dry_run)
    for line in actions:
        print(("would " if args.dry_run else "") + line)
    unchanged = len(labels) - created - updated
    print("%s: %d labels, %d created, %d updated, %d unchanged"
          % ("dry-run" if args.dry_run else repo, len(labels), created, updated, unchanged))
    return 0


if __name__ == "__main__":
    sys.exit(main())
