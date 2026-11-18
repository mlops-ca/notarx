#!/usr/bin/env python3
"""Fixture tests for scripts/oci-free-tier-check.py: the check must pass on the
shipped stack and fail on each way the reference deployment can regress."""
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
DIRECTORY = ROOT / "deploy" / "oci-free-tier"
spec = importlib.util.spec_from_file_location("oci_free_tier_check", ROOT / "scripts" / "oci-free-tier-check.py")
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


def model():
    return json.loads(json.dumps(yaml.safe_load((DIRECTORY / "compose.yml").read_text())))


def model_errors(mutated):
    errors = []
    check.check_model(mutated, errors)
    return errors


class OciFreeTierCheck(unittest.TestCase):
    def test_shipped_stack_passes(self):
        errors = model_errors(model())
        check.check_files(DIRECTORY, errors)
        self.assertEqual(errors, [])

    def test_memory_over_allowance_fails(self):
        mutated = model()
        mutated["services"]["notarx"]["mem_limit"] = "12g"
        self.assertTrue(any("12 GiB" in error for error in model_errors(mutated)))

    def test_missing_limit_fails(self):
        mutated = model()
        del mutated["services"]["caddy"]["mem_limit"]
        self.assertTrue(any("no memory limit" in error for error in model_errors(mutated)))

    def test_adr_limits_are_enforced(self):
        mutated = model()
        mutated["services"]["postgres"]["mem_limit"] = "2g"
        self.assertTrue(any("postgres memory limit" in error for error in model_errors(mutated)))

    def test_postgres_needs_pg_stat_statements(self):
        mutated = model()
        mutated["services"]["postgres"]["command"] = ["postgres"]
        self.assertTrue(any("pg_stat_statements" in error for error in model_errors(mutated)))

    def test_only_caddy_publishes_80_and_443(self):
        mutated = model()
        mutated["services"]["postgres"]["ports"] = ["5432:5432"]
        mutated["services"]["caddy"]["ports"].append("8080:8080")
        errors = model_errors(mutated)
        self.assertTrue(any("postgres publishes" in error for error in errors))
        self.assertTrue(any("exactly 80 and 443" in error for error in errors))

    def test_build_and_foreign_platform_fail(self):
        mutated = model()
        mutated["services"]["notarx"]["build"] = "."
        mutated["services"]["caddy"]["platform"] = "linux/amd64"
        errors = model_errors(mutated)
        self.assertTrue(any("builds from source" in error for error in errors))
        self.assertTrue(any("linux/arm64" in error for error in errors))

    def test_scanner_must_stay_off(self):
        mutated = model()
        mutated["services"]["reference-scanner"] = copy.deepcopy(mutated["services"]["caddy"])
        self.assertTrue(any("scanner" in error for error in model_errors(mutated)))

    def test_object_store_must_be_filesystem(self):
        mutated = model()
        mutated["services"]["notarx"]["environment"]["NOTARX_OBJECT_STORE_DRIVER"] = "s3"
        self.assertTrue(any("filesystem driver" in error for error in model_errors(mutated)))

    def test_database_network_must_be_internal(self):
        mutated = model()
        mutated["networks"]["backend"] = {}
        self.assertTrue(any("internal" in error for error in model_errors(mutated)))

    def test_image_must_be_one_variable_expression(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = pathlib.Path(tmp)
            for name in check.LAYOUT:
                (directory / name).write_text((DIRECTORY / name).read_text())
            compose = directory / "compose.yml"
            compose.write_text(compose.read_text().replace("${NOTARX_IMAGE:-ghcr.io/mlops-ca/notarx:main}", "ghcr.io/mlops-ca/notarx:main"))
            errors = []
            check.check_files(directory, errors)
            self.assertTrue(any("VARIABLE" in error for error in errors))

    def test_secrets_and_missing_layout_fail(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = pathlib.Path(tmp)
            (directory / "compose.yml").write_text("services: {}\n")
            (directory / ".env.example").write_text("NOTARX_ADMIN_TOKEN=hunter2\n")
            (directory / "extra.txt").write_text("-----BEGIN PRIVATE KEY-----\n")
            errors = []
            check.check_files(directory, errors)
            joined = "\n".join(errors)
            self.assertIn("Caddyfile is missing", joined)
            self.assertIn("NOTARX_ADMIN_TOKEN must be empty", joined)
            self.assertIn("contains a private key", joined)


if __name__ == "__main__":
    unittest.main()
