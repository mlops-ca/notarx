#!/usr/bin/env python3
"""Static checks for deploy/oci-free-tier (ADR 0017, slice 6).

Reads the rendered Compose model (docker compose config --format json) on stdin
or from --compose-json, and the raw files from --directory. Checks:

  - every image is one ${VARIABLE:-reference} expression (the release bundle
    renderer, ADR 0025 slice 3, replaces the reference with a digest pin);
  - no service builds from source or pins a platform other than linux/arm64;
  - memory limits sum within the 12 GB A1 allowance and leave 5 GB headroom, with
    the ADR's per-service limits (PostgreSQL 3 GB, notarx 3 GB, console 256 MB);
  - PostgreSQL starts with shared_preload_libraries=pg_stat_statements;
  - only ports 80 and 443 are published and PostgreSQL is not published;
  - the scanner is off; the filesystem driver is the primary store;
  - the files the bundle renderer consumes exist, and no file holds a secret.
"""
import argparse
import json
import pathlib
import re
import sys

GIB = 1 << 30
MIB = 1 << 20
LAYOUT = ["compose.yml", "Caddyfile", ".env.example", "cloud-init.yaml"]
SECRET_NAMES = re.compile(r"(PASSWORD|TOKEN|SECRET|KEY)$")
IMAGE_EXPRESSION = re.compile(r"^\${[A-Z][A-Z0-9_]*:-[^}\s]+}$")
PRIVATE_KEY = re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")
LONG_SECRET = re.compile(r"(?<![A-Za-z0-9+/=_-])[A-Za-z0-9+/_-]{40,}(?![A-Za-z0-9+/=_-])")


def parse_bytes(value):
    if value is None:
        return None
    if isinstance(value, (int, float)):
        return int(value)
    match = re.fullmatch(r"(\d+)([kmgKMG]?)[bB]?", str(value).strip())
    if not match:
        raise ValueError(f"unparseable memory limit {value!r}")
    unit = {"": 1, "k": 1 << 10, "m": MIB, "g": GIB}[match.group(2).lower()]
    return int(match.group(1)) * unit


def published_ports(service):
    result = []
    for port in service.get("ports") or []:
        if isinstance(port, dict):
            result.append(str(port.get("published", "")))
        else:
            parts = str(port).split(":")
            result.append(parts[-2] if len(parts) >= 2 else parts[0])
    return result


def check_model(model, errors):
    services = model.get("services", {})
    for expected in ("postgres", "migrate", "notarx", "notarx-console", "caddy"):
        if expected not in services:
            errors.append(f"service {expected} is missing")
    if any(name in services for name in ("reference-scanner", "scanner")) or any(s.get("profiles") for s in services.values()):
        errors.append("the scanner must stay off: no scanner service or compose profile in this stack")
    total = 0
    limits = {}
    for name, service in services.items():
        if "build" in service:
            errors.append(f"{name} builds from source; the reference stack only runs published images")
        platform = service.get("platform")
        if platform and platform != "linux/arm64":
            errors.append(f"{name} pins platform {platform}; the A1 VM is linux/arm64")
        limit = parse_bytes(service.get("mem_limit"))
        if limit is None:
            limit = parse_bytes(((service.get("deploy") or {}).get("resources") or {}).get("limits", {}).get("memory"))
        if limit is None:
            errors.append(f"{name} has no memory limit")
            continue
        limits[name] = limit
        total += limit
    if total > 12 * GIB:
        errors.append(f"memory limits sum to {total / GIB:.2f} GiB, above the 12 GiB A1 allowance")
    if total > 7 * GIB:
        errors.append(f"memory limits sum to {total / GIB:.2f} GiB, leaving less than 5 GiB for the OS and the page cache")
    for name, want in (("postgres", 3 * GIB), ("notarx", 3 * GIB), ("notarx-console", 256 * MIB)):
        if limits.get(name) != want:
            errors.append(f"{name} memory limit must be {want // MIB} MiB (ADR 0017), found {limits.get(name)}")
    command = " ".join(str(part) for part in services.get("postgres", {}).get("command") or [])
    if "shared_preload_libraries=pg_stat_statements" not in command:
        errors.append("postgres must start with -c shared_preload_libraries=pg_stat_statements")
    for name, service in services.items():
        published = published_ports(service)
        if name == "caddy":
            if sorted(published) != ["443", "80"]:
                errors.append(f"caddy must publish exactly 80 and 443, found {published}")
        elif published:
            errors.append(f"{name} publishes ports {published}; only caddy may")
    environment = services.get("notarx", {}).get("environment") or {}
    if isinstance(environment, list):
        environment = dict(item.split("=", 1) for item in environment)
    if environment.get("NOTARX_OBJECT_STORE_DRIVER") != "filesystem" or environment.get("NOTARX_OBJECT_STORE_FS_ROOT") != "/srv/notarx/objects":
        errors.append("notarx must use the filesystem driver at /srv/notarx/objects")
    backend = (model.get("networks") or {}).get("backend") or {}
    if not backend.get("internal"):
        errors.append("the backend network must be internal")
    if "backend" in (services.get("caddy", {}).get("networks") or {}):
        errors.append("caddy must not be on the backend network")


def check_files(directory, errors):
    for name in LAYOUT:
        if not (directory / name).is_file():
            errors.append(f"{name} is missing from {directory}")
    compose = directory / "compose.yml"
    if compose.is_file():
        for number, line in enumerate(compose.read_text().splitlines(), 1):
            stripped = line.strip()
            if stripped.startswith("image:"):
                value = stripped[len("image:"):].strip()
                if not IMAGE_EXPRESSION.match(value):
                    errors.append(f"compose.yml:{number}: image must be one ${{VARIABLE:-reference}} expression, found {value!r}")
    example = directory / ".env.example"
    if example.is_file():
        for number, line in enumerate(example.read_text().splitlines(), 1):
            if line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            if SECRET_NAMES.search(key) and value.strip():
                errors.append(f".env.example:{number}: {key} must be empty; the example holds no secrets")
    for path in sorted(directory.rglob("*")):
        if not path.is_file() or path.suffix in {".tfstate"} or path.name == ".terraform.lock.hcl":
            continue
        text = path.read_text(errors="replace")
        if PRIVATE_KEY.search(text):
            errors.append(f"{path}: contains a private key")
        for number, line in enumerate(text.splitlines(), 1):
            if "sha256:" in line or line.lstrip().startswith("#"):
                continue
            if LONG_SECRET.search(line) and not re.search(r"https?://|/[a-z]+/[a-z]+", line):
                errors.append(f"{path}:{number}: looks like an embedded secret")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", default="deploy/oci-free-tier")
    parser.add_argument("--compose-json", help="rendered compose model; default: stdin")
    args = parser.parse_args()
    raw = pathlib.Path(args.compose_json).read_text() if args.compose_json else sys.stdin.read()
    errors = []
    check_model(json.loads(raw), errors)
    check_files(pathlib.Path(args.directory), errors)
    if errors:
        for error in errors:
            print(f"oci-free-tier: {error}", file=sys.stderr)
        return 1
    print("oci-free-tier: compose model, resource limits and layout checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
