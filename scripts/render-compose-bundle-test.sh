#!/usr/bin/env bash
# Tests scripts/render-compose-bundle.sh (ADR 0025 slice 3) on throwaway directories: the bundle
# content, digest pinning, determinism, the refusals, the absence of secrets and, when Docker is
# available, "docker compose config" on the extracted bundle. Nothing here touches the checkout.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

# shellcheck source=lib/test-helpers.sh
source "$root/scripts/lib/test-helpers.sh"

d1=sha256:$(printf 'a%.0s' $(seq 64))
d2=sha256:$(printf 'b%.0s' $(seq 64))
render=$root/scripts/render-compose-bundle.sh

expect_ok 'renders a bundle' "$render" --version 0.1.0 --output "$workdir/out1" --image "notarx=$d1" --image "notarx-console=$d2"
bundle=$workdir/out1/notarx-compose-0.1.0.tar.gz
[[ -f "$bundle" ]] || fail 'the bundle was not written'
mkdir "$workdir/x"
tar -xzf "$bundle" -C "$workdir/x"
dir=$workdir/x/notarx-compose-0.1.0
for f in compose.yml Caddyfile .env.example cloud-init.yaml run-migrations.sh README.md migrations/000001_baseline.sql; do
  cases=$((cases + 1))
  [[ -f "$dir/$f" ]] || fail "the bundle lacks $f"
done
cases=$((cases + 1))
[[ -x "$dir/run-migrations.sh" ]] || fail 'run-migrations.sh is not executable'
expect_eq 'migrations are the repository migrations' "$(cd "$root/migrations" && find . -type f | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256)" "$(cd "$dir/migrations" && find . -type f | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256)"
cases=$((cases + 1))
cmp -s "$root/scripts/run-migrations.sh" "$dir/run-migrations.sh" || fail 'run-migrations.sh differs from the source'

# Every image is pinned by digest; the project images by the digests given.
images=$(grep -E '^[[:space:]]*image:' "$dir/compose.yml")
expect_eq 'every image line carries a digest' "$(grep -c -v -E '@sha256:[0-9a-f]{64}' <<<"$images" || true)" 0
cases=$((cases + 1))
grep -q "ghcr.io/mlops-ca/notarx@$d1" "$dir/compose.yml" || fail 'notarx is not pinned to its digest'
cases=$((cases + 1))
grep -q "ghcr.io/mlops-ca/notarx-console@$d2" "$dir/compose.yml" || fail 'notarx-console is not pinned to its digest'
cases=$((cases + 1))
if grep -q ':main' "$dir/compose.yml"; then fail 'a :main tag is left in compose.yml'; fi
cases=$((cases + 1))
if grep -q 'Running from a repository clone' "$dir/.env.example"; then fail '.env.example keeps the clone-only settings'; fi
# No secret: every secret setting is empty (or commented), and no key material appears anywhere.
cases=$((cases + 1))
if grep -r -E '^[A-Z0-9_]*(PASSWORD|TOKEN|SECRET|_KEY)[A-Z0-9_]*=.+' "$dir" >/dev/null; then fail 'a secret setting has a value'; fi
cases=$((cases + 1))
if grep -r -l -E 'BEGIN [A-Z ]*PRIVATE KEY' "$dir" >/dev/null; then fail 'private key material in the bundle'; fi
cases=$((cases + 1))
grep -q "ghcr.io/mlops-ca/notarx@$d1" "$dir/README.md" || fail 'the README does not list the image digests'

# The documented extraction (docs/install.md, the runbook) flattens the bundle into the target directory.
mkdir "$workdir/flat"
tar -xzf "$bundle" -C "$workdir/flat" --strip-components=1
cases=$((cases + 1))
[[ -f "$workdir/flat/compose.yml" && -f "$workdir/flat/.env.example" && -d "$workdir/flat/migrations" ]] || fail '--strip-components=1 does not flatten the bundle'
cases=$((cases + 1))
grep -q 'tar -xzf "notarx-compose-$version.tar.gz" -C /opt/notarx --strip-components=1' "$root/docs/install.md" || fail 'docs/install.md no longer documents the strip-components extraction this test proves'
cases=$((cases + 1))
grep -q 'sudo tar -xzf "notarx-compose-$version.tar.gz" -C /opt/notarx --strip-components=1' "$root/docs/deployment/oci-free-tier.md" || fail 'the runbook no longer documents the strip-components extraction this test proves'

# The tar holds one top-level directory, with fixed owners and no timestamps from the build.
expect_eq 'one top-level directory' "$(tar -tzf "$bundle" | sed -e 's|^\./||' -e 's|/.*||' | sort -u | grep -c -v -E '^\.?$')" 1

# The stable-only version identity gate (scripts/check-no-nightly.sh) accepts the bundle: it is a
# .tar.gz asset like the archives.
expect_ok 'the version identity gate accepts the bundle' "$root/scripts/check-no-nightly.sh" --dir "$workdir/out1"

# Deterministic: the same inputs give the same bytes.
expect_ok 'renders again' "$render" --version 0.1.0 --output "$workdir/out2" --image "notarx-console=$d2" --image "notarx=$d1"
expect_eq 'the bundle is deterministic' "$(shasum -a 256 <"$bundle")" "$(shasum -a 256 <"$workdir/out2/notarx-compose-0.1.0.tar.gz")"

# The README tells users how to prepare the data directories.
cases=$((cases + 1))
grep -q 'chown 65532:65532 /srv/notarx/objects' "$dir/README.md" || fail 'the README does not say how to prepare the data directories'

# The docker model: required variables come from a placeholder .env, as in the images check.
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  cat >"$workdir/placeholder.env" <<'ENV'
NOTARX_DOMAIN=notarx.example.invalid
NOTARX_ACME_EMAIL=ops@example.invalid
NOTARX_POSTGRES_PASSWORD=placeholder-postgres
NOTARX_ADMIN_TOKEN=placeholder-admin
NOTARX_RESOLVER_TOKEN=placeholder-resolver
ENV
  expect_ok 'docker compose config passes on the bundle' docker compose --project-directory "$dir" --env-file "$workdir/placeholder.env" -f "$dir/compose.yml" config --quiet
  cases=$((cases + 1))
  resolved=$(docker compose --project-directory "$dir" --env-file "$workdir/placeholder.env" -f "$dir/compose.yml" config --images)
  grep -q "ghcr.io/mlops-ca/notarx@$d1" <<<"$resolved" || fail 'compose config does not resolve the pinned image'
else
  printf 'SKIPPED docker compose config: docker compose is not installed (CI runs it)\n'
fi

# Refusals.
expect_fail 'a nightly version' 'stable X.Y.Z' "$render" --version 0.1.0-nightly.3 --output "$workdir/o" --image "notarx=$d1" --image "notarx-console=$d2"
expect_fail 'a missing digest' 'no digest given for ghcr.io/mlops-ca/notarx-console' "$render" --version 0.1.0 --output "$workdir/o" --image "notarx=$d1"
expect_fail 'a tag instead of a digest' 'no usable index digest' "$render" --version 0.1.0 --output "$workdir/o" --image notarx=latest --image "notarx-console=$d2"
expect_fail 'a duplicate image' 'given twice' "$render" --version 0.1.0 --output "$workdir/o" --image "notarx=$d1" --image "notarx=$d2" --image "notarx-console=$d2"
expect_fail 'an unused image' 'does not use it' "$render" --version 0.1.0 --output "$workdir/o" --image "notarx=$d1" --image "notarx-console=$d2" --image "other=$d1"
expect_ok 'the optional scanner image is accepted' "$render" --version 0.1.0 --output "$workdir/o3" --image "notarx=$d1" --image "notarx-console=$d2" --image "notarx-scanner=$d1"
# --registry points the pins at another registry (a fork or a mirror); the source prefix is still recognised.
expect_ok 'another registry' "$render" --version 0.1.0 --output "$workdir/o4" --registry ghcr.io/example --image "notarx=$d1" --image "notarx-console=$d2"
mkdir "$workdir/y"
tar -xzf "$workdir/o4/notarx-compose-0.1.0.tar.gz" -C "$workdir/y"
cases=$((cases + 1))
grep -q "ghcr.io/example/notarx@$d1" "$workdir/y/notarx-compose-0.1.0/compose.yml" || fail '--registry is not applied to the pins'
expect_fail 'no output' 'output is required' "$render" --version 0.1.0 --image "notarx=$d1" --image "notarx-console=$d2"
expect_fail 'an unknown argument' 'unknown argument' "$render" --bogus x

# A third-party image that is not pinned by digest, and a secret value in .env.example.
fake=$workdir/fake
mkdir -p "$fake/deploy" "$fake/scripts"
cp -R "$root/deploy/oci-free-tier" "$fake/deploy/oci-free-tier"
cp -R "$root/migrations" "$fake/migrations"
cp "$root/scripts/run-migrations.sh" "$fake/scripts/"
sed -i.bak -E 's|(NOTARX_CADDY_IMAGE:-caddy:2.11-alpine)@sha256:[0-9a-f]+|\1|' "$fake/deploy/oci-free-tier/compose.yml"
rm -f "$fake/deploy/oci-free-tier/compose.yml.bak"
expect_fail 'an unpinned third-party image' 'not pinned by digest in deploy/oci-free-tier/compose.yml' env REPO_ROOT="$fake" "$render" --version 0.1.0 --output "$workdir/o" --image "notarx=$d1" --image "notarx-console=$d2"
cp "$root/deploy/oci-free-tier/compose.yml" "$fake/deploy/oci-free-tier/compose.yml"
sed -i.bak 's|^NOTARX_ADMIN_TOKEN=$|NOTARX_ADMIN_TOKEN=hunter2|' "$fake/deploy/oci-free-tier/.env.example"
rm -f "$fake/deploy/oci-free-tier/.env.example.bak"
expect_fail 'a secret in .env.example' 'carries a secret value' env REPO_ROOT="$fake" "$render" --version 0.1.0 --output "$workdir/o" --image "notarx=$d1" --image "notarx-console=$d2"

# The runbook's layout section names every file of the directory and of the bundle.
runbook=$root/docs/deployment/oci-free-tier.md
for name in compose.yml Caddyfile .env.example cloud-init.yaml tofu/ run-migrations.sh migrations/; do
  cases=$((cases + 1))
  grep -q "\`$name\`" "$runbook" || fail "the runbook layout table does not name $name"
done
for entry in "$root"/deploy/oci-free-tier/* "$root"/deploy/oci-free-tier/.env.example; do
  name=$(basename "$entry")
  [[ -d "$entry" ]] && name=$name/
  cases=$((cases + 1))
  grep -q "\`$name\`" "$runbook" || fail "the runbook layout table does not name the directory entry $name"
done

finish render-compose-bundle-test
