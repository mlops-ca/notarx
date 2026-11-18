#!/usr/bin/env python3
"""Fail when a non-test Go file under internal/ or cmd/ carries CJK text (ADR 0014).

Servers return stable codes and English messages; the Console localizes by
code. Test files are free to use any text. Files listed in
scripts/check-no-cjk-go.allow (under the scanned root) are exempt until the change that owns them
lands; each line there names the file and the ADR slice that removes the text.

    python3 scripts/check-no-cjk-go.py            # scan the repository
    python3 scripts/check-no-cjk-go.py ROOT       # scan another tree
"""
import re
import sys
from pathlib import Path

CJK = re.compile('[\u3400-\u9fff\uf900-\ufaff\u3000-\u303f\uff00-\uffef]')
SCAN = ('internal', 'cmd')


def allowed(root):
    """The exempt paths from scripts/check-no-cjk-go.allow under root."""
    path = Path(root) / 'scripts' / 'check-no-cjk-go.allow'
    if not path.exists():
        return set()
    rows = [row.split('#')[0].strip() for row in path.read_text().splitlines()]
    return {row for row in rows if row}


def findings(root):
    root = Path(root)
    exempt = allowed(root)
    found = []
    for top in SCAN:
        for path in sorted((root / top).rglob('*.go')) if (root / top).is_dir() else []:
            relative = path.relative_to(root).as_posix()
            if path.name.endswith('_test.go') or relative in exempt:
                continue
            for number, line in enumerate(path.read_text(encoding='utf-8').splitlines(), 1):
                if CJK.search(line):
                    found.append(f'{relative}:{number}: CJK text in a non-test Go file')
    return found


def main(argv):
    root = argv[1] if len(argv) > 1 else Path(__file__).resolve().parent.parent
    issues = findings(root)
    for issue in issues:
        print(issue)
    if issues:
        print('Server text must be English with a stable code; the Console localizes (ADR 0014).')
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
