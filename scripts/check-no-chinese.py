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

CJK = re.compile('[\u3400-\u9fff\uf900-\ufaff]')
ZH_PATH = re.compile(r'(^|/)(zh-CN|zh_CN)(/|\.)|\.zh-CN\.|[-_.]zh-CN$', re.I)


def path_issues(entries):
    """entries: (status, path) pairs from git diff --name-status."""
    return [f'{path}: zh-CN files must not be added or modified'
            for status, path in entries if status[0] != 'D' and ZH_PATH.search(path)]


def line_issues(diff_text):
    issues, current = [], None
    for line in diff_text.splitlines():
        if line.startswith('+++ '):
            current = line[4:].removeprefix('b/')
        elif line.startswith('+') and CJK.search(line):
            issues.append(f'{current}: added line contains Chinese text')
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
