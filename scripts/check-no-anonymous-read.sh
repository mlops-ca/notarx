#!/usr/bin/env bash
# ADR 0029, slice 1: the boolean anonymousRead is gone. The visibility enum
# (private, authenticated, public) replaced it with no alias in the API, store,
# CLI or Console, and the anonymous-access-policy endpoint became
# public-access-policy. This gate fails when a retired name returns.
#
# Not scanned: ADRs and release records (history), reports, the immutable
# baseline migration and its tooling fixtures, the migration that performs the
# rename and its test, the Console message catalogs (message ids of retired
# keys stay in the frozen zh-CN catalog, ADR 0014) and this script.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

pattern='\banonymousRead\b|\banonymous_read\b|\bAnonymousRead\b|--anonymous-read\b|anonymous-access-policy|\bAnonymousAccessPolicy\b|\banonymous_access_policy\b'

hits=$(git grep -nIE "$pattern" -- . \
  ':(exclude)docs/adr' \
  ':(exclude)docs/release-records' \
  ':(exclude)docs/reports' \
  ':(exclude)migrations' \
  ':(exclude)internal/tools/baselinefmt' \
  ':(exclude)console/src/messages' \
  ':(exclude)internal/repository/postgres_visibility_migration_integration_test.go' \
  ':(exclude)scripts/check-no-anonymous-read.sh' \
  ':(exclude)*.zh-CN.*' || true)

if [[ -n "$hits" ]]; then
  printf 'retired anonymous-read names found (use visibility and public-access-policy, ADR 0029):\n%s\n' "$hits" >&2
  exit 1
fi

# The regenerated baseline (ADR 0015 slice 7) must carry the visibility enum and
# none of the retired names, so the generator never reintroduces them. The only
# tolerated mention is the name of a NOT NULL constraint: PostgreSQL 18 keeps the
# constraint names of a renamed table (anonymous_access_policy_*_not_null).
baseline=migrations/000001_baseline.sql
if [[ -f "$baseline" ]]; then
  grep -q 'visibility character varying' "$baseline" || { printf '%s has no visibility column\n' "$baseline" >&2; exit 1; }
  stale=$(grep -nE 'anonymous_read|anonymous_access_policy' "$baseline" | grep -vE 'anonymous_access_policy_[a-z_]+_not_null' || true)
  if [[ -n "$stale" ]]; then
    printf 'retired names in %s:\n%s\n' "$baseline" "$stale" >&2
    exit 1
  fi
fi
printf 'anonymous-read removal check: pass\n'
