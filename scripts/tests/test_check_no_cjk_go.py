import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / 'check-no-cjk-go.py'
spec = importlib.util.spec_from_file_location('check_no_cjk_go', SCRIPT)
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

CJK_TEXT = '\u6ca1\u6709\u5728\u7ebf'  # escaped so this file holds no CJK literal


def tree(files):
    root = tempfile.TemporaryDirectory()
    for name, text in files.items():
        path = Path(root.name) / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding='utf-8')
    return root


class CheckNoCJKGo(unittest.TestCase):
    def test_flags_a_non_test_go_file(self):
        with tree({'internal/app/a.go': f'package app\nvar m = "{CJK_TEXT}"\n'}) as root:
            self.assertEqual(check.findings(root), ['internal/app/a.go:2: CJK text in a non-test Go file'])

    def test_flags_comments_and_cmd(self):
        with tree({'cmd/x/main.go': f'package main\n// {CJK_TEXT}\n'}) as root:
            self.assertEqual(len(check.findings(root)), 1)

    def test_allows_test_files_and_english(self):
        with tree({
            'internal/app/a_test.go': f'package app\nvar m = "{CJK_TEXT}"\n',
            'internal/app/a.go': 'package app\nvar m = "No online API nodes"\n',
        }) as root:
            self.assertEqual(check.findings(root), [])

    def test_honours_the_allow_list_of_the_scanned_root(self):
        files = {
            'internal/a/exempt.go': f'package a\nvar m = "{CJK_TEXT}"\n',
            'internal/a/flagged.go': f'package a\nvar m = "{CJK_TEXT}"\n',
            'scripts/check-no-cjk-go.allow': '# owned by a later slice\ninternal/a/exempt.go  # slice 7\n',
        }
        with tree(files) as root:
            self.assertEqual(check.allowed(root), {'internal/a/exempt.go'})
            self.assertEqual(check.findings(root), ['internal/a/flagged.go:2: CJK text in a non-test Go file'])

    def test_another_roots_allow_list_does_not_leak(self):
        with tree({'internal/emailnotification/template.go': f'package e\nvar m = "{CJK_TEXT}"\n'}) as root:
            self.assertEqual(check.allowed(root), set())
            self.assertEqual(len(check.findings(root)), 1)

    def test_the_repository_exempts_nothing(self):
        # The email bodies moved into the embedded locale bundles (ADR 0014 slice 7).
        self.assertEqual(check.allowed(SCRIPT.parent.parent), set())

    def test_the_repository_is_clean(self):
        self.assertEqual(check.findings(SCRIPT.parent.parent), [])


if __name__ == '__main__':
    unittest.main()
