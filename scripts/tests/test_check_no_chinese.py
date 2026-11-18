import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('check_no_chinese', ROOT / 'scripts/check-no-chinese.py')
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)

HAN = '\u7b80\u4f53\u4e2d\u6587'


class NoChineseGuardTests(unittest.TestCase):
    def test_added_or_modified_zh_cn_files_fail(self):
        for status in ('A', 'M', 'T'):
            for path in ('docs/ci.zh-CN.md', 'CONTRIBUTING.zh-CN.md', 'console/src/locales/zh-CN/app.json',
                         'console/src/i18n/zh-CN.ts'):
                with self.subTest(status=status, path=path):
                    self.assertEqual(len(guard.path_issues([(status, path)])), 1)

    def test_zh_cn_file_diff_that_only_removes_lines_passes(self):
        entries = [('M', 'console/src/messages/ui.zh-CN.ts'), ('M', 'docs/ci.zh-CN.md')]
        self.assertEqual(guard.path_issues(entries, {}), [])
        self.assertEqual(guard.path_issues(entries, {'docs/other.md': 3}), [])

    def test_zh_cn_file_diff_that_adds_or_changes_lines_fails(self):
        entries = [('M', 'console/src/messages/ui.zh-CN.ts')]
        self.assertEqual(len(guard.path_issues(entries, {'console/src/messages/ui.zh-CN.ts': 1})), 1)
        self.assertEqual(len(guard.path_issues([('A', 'console/src/messages/new.zh-CN.ts')], {})), 1)

    def test_added_line_counts_separates_files_and_ignores_headers(self):
        diff = ('diff --git a/a.zh-CN.ts b/a.zh-CN.ts\n--- a/a.zh-CN.ts\n+++ b/a.zh-CN.ts\n@@ -2,2 +2,0 @@\n'
                '-  one: "1",\n-  two: "2",\n'
                'diff --git a/b.zh-CN.ts b/b.zh-CN.ts\n--- a/b.zh-CN.ts\n+++ b/b.zh-CN.ts\n@@ -1 +1 @@\n'
                f'-  x: "{HAN}",\n+  x: "{HAN}!",\n')
        self.assertEqual(guard.added_line_counts(diff), {'b.zh-CN.ts': 1})

    def test_deleting_zh_cn_files_is_allowed(self):
        self.assertEqual(guard.path_issues([('D', 'docs/ci.zh-CN.md'), ('D', 'console/src/locales/zh-CN/app.json')]), [])

    def test_ordinary_paths_pass(self):
        self.assertEqual(guard.path_issues([('M', 'docs/ci.md'), ('A', 'scripts/check-no-chinese.py')]), [])

    def test_added_chinese_text_fails_but_removed_text_passes(self):
        diff = ('diff --git a/docs/site-map.json b/docs/site-map.json\n'
                '--- a/docs/site-map.json\n+++ b/docs/site-map.json\n@@ -1 +1 @@\n'
                f'-  "title": "{HAN}"\n+  "title": "English"\n')
        self.assertEqual(guard.line_issues(diff), [])
        added = diff.replace('-  "title"', '+  "x"', 1)
        self.assertEqual(guard.line_issues(added), ['docs/site-map.json: added line contains Chinese text'])

    def test_added_line_starting_with_plus_plus_is_content_not_a_header(self):
        diff = ('diff --git a/a.md b/a.md\n--- a/a.md\n+++ b/a.md\n@@ -0,0 +2 @@\n'
                f'+++ x\n+{HAN}\n')
        self.assertEqual(guard.line_issues(diff), ['a.md: added line contains Chinese text'])


if __name__ == '__main__':
    unittest.main()
