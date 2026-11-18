#!/usr/bin/env python3
"""Fail when a Go test file names an external (non-local) host in a URL literal.

No CI test may depend on an external service (issue #333): tests serve what they
need from an in-process or loopback fixture. A URL literal to a public host in a
test file is either a dial target (forbidden) or test data. Existing mentions are allowed by listing the file in scripts/external-network-test-baseline.txt,
one "path host" pair per line. Baselined pairs are NOT proven to be data: review them
when touching those files. The check enforces no new file/host pairs, and the baseline may only shrink: an entry that no
longer matches is reported so it gets removed.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
BASELINE = ROOT / 'scripts' / 'external-network-test-baseline.txt'
URL = re.compile(r'https?://([A-Za-z0-9][A-Za-z0-9.-]*)')
LOCAL_SUFFIXES = ('.example.com', '.example.org', '.example.net', '.test', '.example', '.invalid', '.local', '.internal', '.localhost', '.svc', '.cluster.local')
LOCAL_HOSTS = {'localhost', 'example.com', 'example.org', 'example.net', 'registry', 'gateway'}


def local(host):
    host = host.lower().rstrip('.')
    if host in LOCAL_HOSTS or host.endswith(LOCAL_SUFFIXES) or '.' not in host:
        return True
    return bool(re.fullmatch(r'(127|10)(\.\d+){3}|192\.168(\.\d+){2}|172\.(1[6-9]|2\d|3[01])(\.\d+){2}|169\.254(\.\d+){2}|0\.0\.0\.0', host))


def findings():
    found = set()
    for path in sorted(ROOT.glob('**/*_test.go')):
        rel = path.relative_to(ROOT).as_posix()
        if rel.startswith(('console/node_modules/', '.claude/')):
            continue
        text = path.read_text(encoding='utf-8', errors='replace')
        for host in URL.findall(text):
            if not local(host):
                found.add(f'{rel} {host.lower()}')
    return found


def main():
    baseline = {line.strip() for line in BASELINE.read_text().splitlines() if line.strip() and not line.startswith('#')} if BASELINE.exists() else set()
    found = findings()
    if '--update' in sys.argv:
        BASELINE.write_text('# Test files that name external hosts (test data, never dialed). Shrink only.\n' + '\n'.join(sorted(found)) + '\n')
        return 0
    new = sorted(found - baseline)
    stale = sorted(baseline - found)
    for entry in new:
        print(f'external host in test (tests must use a loopback fixture): {entry}')
    for entry in stale:
        print(f'stale baseline entry, remove it: {entry}')
    return 1 if new or stale else 0


if __name__ == '__main__':
    sys.exit(main())
