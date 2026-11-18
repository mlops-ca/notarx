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

    def test_deleting_zh_cn_files_is_allowed(self):
        self.assertEqual(guard.path_issues([('D', 'docs/ci.zh-CN.md'), ('D', 'console/src/locales/zh-CN/app.json')]), [])

    def test_ordinary_paths_pass(self):
        self.assertEqual(guard.path_issues([('M', 'docs/ci.md'), ('A', 'scripts/check-no-chinese.py')]), [])

    def test_added_chinese_text_fails_but_removed_text_passes(self):
        diff = ('--- a/docs/site-map.json\n+++ b/docs/site-map.json\n'
                f'-  "title": "{HAN}"\n+  "title": "English"\n')
        self.assertEqual(guard.line_issues(diff), [])
        added = diff.replace('-  "title"', '+  "x"', 1)
        self.assertEqual(guard.line_issues(added), ['docs/site-map.json: added line contains Chinese text'])


if __name__ == '__main__':
    unittest.main()
