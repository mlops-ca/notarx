#!/usr/bin/env python3
"""Fail a change that adds or modifies Chinese content (owner rule, 2026-11-18).

Flags added or modified zh-CN files (*.zh-CN.md and zh-CN locale files) and any
added line that contains Chinese characters. Deletions stay allowed. In CI the
change is the pull request merge commit against its first parent:

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
        rows = open(ALLOW).read().splitlines()
    except FileNotFoundError:
        return set()
    return {tuple(row.split()) for row in rows if row.strip() and not row.startswith('#')}


def blob(path):
    result = subprocess.run(['git', 'rev-parse', 'HEAD:' + path], capture_output=True, text=True)
    return result.stdout.strip()


def path_issues(entries):
    """entries: (status, path) pairs from git diff --name-status."""
    exempt = allowed()
    return [f'{path}: zh-CN files must not be added or modified'
            for status, path in entries
            if status[0] != 'D' and ZH_PATH.search(path) and (path, blob(path)) not in exempt]


CJK_RUN = re.compile('[\u3400-\u9fff\uf900-\ufaff]+')


def line_issues(diff_text):
    """Flag added lines with Chinese text that no removed line of the change had.

    Editing the non-Chinese part of a line (for example an English title next
    to a frozen zh-CN title) or moving a file re-adds the same Chinese text;
    that adds no new Chinese content, so it is allowed.
    """
    issues, current, removed, added = [], None, set(), []
    exempt = {path for path, sha in allowed() if blob(path) == sha}

    for line in diff_text.splitlines():
        if line.startswith('+++ '):
            current = line[4:].removeprefix('b/')
        elif line.startswith('--- '):
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
    names = subprocess.check_output(['git', 'diff', '--name-status', '--no-renames', *span], text=True)
    entries = [tuple(row.split('\t', 1)) for row in names.splitlines() if row]
    diff = subprocess.check_output(['git', 'diff', '--no-renames', '--unified=0', *span], text=True)
    issues = path_issues(entries) + line_issues(diff)
    for issue in issues:
        print(issue, file=sys.stderr)
    return 1 if issues else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
