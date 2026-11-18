#!/usr/bin/env python3
"""Fail a change that adds or modifies Chinese content (owner rule, 2026-11-18).

Flags added zh-CN files (*.zh-CN.md and zh-CN locale files), zh-CN files whose
diff adds any line, and any added line that contains Chinese characters.
Deletions stay allowed, including a diff that only removes lines from an
existing zh-CN file (key pruning, ADR 0014). In CI the change is the pull
request merge commit against its first parent:

    python3 scripts/check-no-chinese.py            # diff HEAD^1..HEAD
    python3 scripts/check-no-chinese.py BASE HEAD  # diff BASE..HEAD
"""
import re
import subprocess
import sys
from pathlib import Path

ALLOW = Path(__file__).with_suffix('.allow')
CJK = re.compile('[\u3400-\u9fff\uf900-\ufaff]')
ZH_PATH = re.compile(r'(^|/)(zh-CN|zh_CN)(/|\.)|\.zh-CN\.|[-_.]zh-CN$', re.I)


def allowed():
    """Exact (path, blob) pairs from scripts/check-no-chinese.allow."""
    try:
        rows = ALLOW.read_text().splitlines()
    except FileNotFoundError:
        return set()
    return {tuple(row.split()) for row in rows if row.strip() and not row.startswith('#')}


def blob(path):
    result = subprocess.run(['git', 'rev-parse', 'HEAD:' + path], capture_output=True, text=True)
    return result.stdout.strip()


def path_issues(entries, added_lines=None):
    """entries: (status, path) pairs from git diff --name-status.

    added_lines maps a path to its count of added lines (see added_line_counts).
    A modified zh-CN file passes when the diff only removes lines from it; when
    the map is not given every modification is flagged.
    """
    exempt = allowed()
    issues = []
    for status, path in entries:
        if status[0] == 'D' or not ZH_PATH.search(path) or (path, blob(path)) in exempt:
            continue
        if status[0] == 'M' and added_lines is not None and not added_lines.get(path, 0):
            continue
        issues.append(f'{path}: zh-CN files must not be added or modified')
    return issues


def added_line_counts(diff_text):
    """Added line count per file of a unified diff."""
    counts, current, in_hunk = {}, None, False
    for line in diff_text.splitlines():
        if line.startswith('diff --git '):
            in_hunk = False
        elif line.startswith('@@'):
            in_hunk = True
        elif not in_hunk and line.startswith('+++ '):
            current = line[4:].removeprefix('b/')
        elif in_hunk and line.startswith('+') and current:
            counts[current] = counts.get(current, 0) + 1
    return counts


CJK_RUN = re.compile('[\u3400-\u9fff\uf900-\ufaff]+')


def line_issues(diff_text):
    """Flag added lines with Chinese text that no removed line of the change had.

    Editing the non-Chinese part of a line (for example an English title next
    to a frozen zh-CN title) or moving a file re-adds the same Chinese text;
    that adds no new Chinese content, so it is allowed.
    """
    issues, current, removed, added = [], None, set(), []
    exempt = {path for path, sha in allowed() if blob(path) == sha}

    in_hunk = False
    for line in diff_text.splitlines():
        # File headers sit between "diff --git" and the first hunk; inside a
        # hunk a line that starts with "++ " or "-- " is content, not a header.
        if line.startswith('diff --git '):
            in_hunk = False
        elif line.startswith('@@'):
            in_hunk = True
        elif not in_hunk and line.startswith('+++ '):
            current = line[4:].removeprefix('b/')
        elif not in_hunk and line.startswith('--- '):
            continue
        elif line.startswith('-'):
            removed.update(CJK_RUN.findall(line))
        elif line.startswith('+') and CJK.search(line) and current not in exempt:
            added.append((current, line))
    for path, line in added:
        if not all(run in removed for run in CJK_RUN.findall(line)):
            issues.append(f'{path}: added line contains Chinese text')
    return sorted(set(issues))


def main(argv):
    span = argv[1:3] if len(argv) >= 3 else ['HEAD^1', 'HEAD']
    names = subprocess.check_output(['git', '-c', 'core.quotepath=off', 'diff', '--name-status', '--no-renames', *span], text=True)
    entries = [tuple(row.split('\t', 1)) for row in names.splitlines() if row]
    diff = subprocess.check_output(['git', '-c', 'core.quotepath=off', 'diff', '--no-renames', '--unified=0', *span], text=True)
    issues = path_issues(entries, added_line_counts(diff)) + line_issues(diff)
    for issue in issues:
        print(issue, file=sys.stderr)
    return 1 if issues else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))