import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    'check_no_external_network_tests',
    pathlib.Path(__file__).resolve().parents[1] / 'check-no-external-network-tests.py',
)
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)


class LocalHostTest(unittest.TestCase):
    def test_local_hosts_are_allowed(self):
        for host in ['localhost', '127.0.0.1', '10.1.2.3', '172.20.0.5', '169.254.1.1', 'gw.example.com', 'a.test', 'registry']:
            self.assertTrue(module.local(host), host)

    def test_public_hosts_are_flagged(self):
        for host in ['repo.maven.apache.org', 'index.crates.io', '8.8.8.8', '172.32.0.1']:
            self.assertFalse(module.local(host), host)


if __name__ == '__main__':
    unittest.main()
