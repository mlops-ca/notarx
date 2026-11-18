#!/usr/bin/env sh
set -eu

compatibility_doc="docs/protocol-compatibility.md"
nexus_doc="docs/nexus-gap-analysis.md"
readiness_doc="docs/release-readiness.md"

contains_fixed_string() {
  needle=$1
  file=$2

  if command -v rg >/dev/null 2>&1; then
    rg -qi --fixed-strings "$needle" "$file"
    return
  fi

  grep -Fqi -- "$needle" "$file"
}

for file in \
  README.md \
  ARCHITECTURE.md \
  CONTRIBUTING.md \
  docs/README.md \
  docs/getting-started.md \
  docs/architecture-diagrams.md \
  docs/postgresql-capabilities.md \
  docs/documentation-site-guide.md \
  docs/site-map.json \
  docs/performance-baseline.md \
  docs/maven-hosted-publication.md \
  docs/project-quality-assessment.md \
  docs/recovery-runbook.md \
  docs/assets/notarx-hero.png \
  docs/assets/notarx-system-architecture.png \
  docs/assets/notarx-deployment-topology.png; do
  if [ ! -s "$file" ]; then
    printf 'missing required documentation entry %s\n' "$file" >&2
    exit 1
  fi
done

if ! contains_fixed_string 'PostgreSQL is the only coordination and database dependency' README.md ||
  ! contains_fixed_string 'S3-compatible object-storage' README.md; then
  printf 'README lightweight storage boundary is missing or misleading\n' >&2
  exit 1
fi

if ! contains_fixed_string 'standard direct publication' README.md ||
  ! contains_fixed_string 'mavenStrictPublication' docs/maven-hosted-publication.md; then
  printf 'Maven direct-by-default and strict opt-in documentation is missing\n' >&2
  exit 1
fi

if ! contains_fixed_string 'site-map.json' docs/README.md; then
  printf 'documentation indexes must disclose the site navigation contract\n' >&2
  exit 1
fi

for file in README.md docs/architecture-diagrams.md; do
  if ! contains_fixed_string 'notarx-system-architecture.png' "$file"; then
    printf 'missing generated system architecture from %s\n' "$file" >&2
    exit 1
  fi
done

for file in docs/architecture-diagrams.md; do
  if ! contains_fixed_string 'notarx-deployment-topology.png' "$file"; then
    printf 'missing generated deployment topology from %s\n' "$file" >&2
    exit 1
  fi
done

for file in README.md docs/getting-started.md; do
  if ! contains_fixed_string 'make dev-bootstrap' "$file"; then
    printf 'missing executable bootstrap path from %s\n' "$file" >&2
    exit 1
  fi
done

node --test scripts/docs-link-check.test.mjs scripts/docs-i18n-check.test.mjs
node scripts/docs-link-check.mjs
node scripts/docs-i18n-check.mjs

for format in OCI Maven Raw Conan npm PyPI Go; do
  if ! contains_fixed_string "$format" "$compatibility_doc"; then
    printf 'missing format %s from %s\n' "$format" "$compatibility_doc" >&2
    exit 1
  fi
done

for required in \
  'repositories:intelligence' \
  'versioned admission policies' \
  'automatic scanner execution'; do
  if ! contains_fixed_string "$required" "$nexus_doc"; then
    printf 'missing capability statement %s from %s\n' "$required" "$nexus_doc" >&2
    exit 1
  fi
done

if ! contains_fixed_string 'npm, PyPI, and Go Hosted lifecycle paths' "$readiness_doc"; then
  printf 'release readiness document has stale hosted format coverage\n' >&2
  exit 1
fi

printf '%s\n' 'documentation capability checks passed'
