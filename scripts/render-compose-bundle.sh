#!/usr/bin/env bash
# Renders deploy/oci-free-tier into the compose bundle release asset (ADR 0025 slice 3).
#
#   scripts/render-compose-bundle.sh --version X.Y.Z --output DIR \
#     --image notarx=sha256:<hex> --image notarx-console=sha256:<hex> [--image notarx-scanner=sha256:<hex>] \
#     [--registry ghcr.io/mlops-ca]
#
# Writes DIR/notarx-compose-X.Y.Z.tar.gz holding one directory notarx-compose-X.Y.Z/ with
# compose.yml, Caddyfile, .env.example, cloud-init.yaml, migrations/, run-migrations.sh and a README.
# Every project image reference of compose.yml becomes <registry>/<image>@<digest> of the
# multi-arch index of the run that built it, so a moved tag cannot change a deployed stack; the
# third-party images must already be pinned by digest in the source. The bundle is produced for a
# stable run only (a nightly version is refused), carries no secret and is deterministic.
# --registry is where the pinned images are pulled from (default: the project's own registry); the
# project images of compose.yml are recognised by the project registry prefix below. REPO_ROOT
# overrides the source tree (tests).
set -euo pipefail

# The registry the images of deploy/oci-free-tier/compose.yml are written against.
source_registry=ghcr.io/mlops-ca
root=${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
script_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=lib/tar-gz.sh
source "$script_root/scripts/lib/tar-gz.sh"

fail() {
  printf 'render-compose-bundle: %s\n' "$1" >&2
  exit 1
}

version=''
output=''
registry=ghcr.io/mlops-ca
declare -a image_args=()
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --version) version=$2 ;;
    --output) output=$2 ;;
    --registry) registry=$2 ;;
    --image) image_args+=("$2") ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version must be a stable X.Y.Z (the bundle is not built for a nightly), got '$version'"
[[ -n "$output" ]] || fail '--output is required'
[[ "$registry" =~ ^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$ ]] || fail "registry must look like ghcr.io/owner, got '$registry'"

# Bash 3.2 (macOS) has no associative arrays: two parallel lists and a lookup.
names=()
digests=()
digest_of() { # digest_of <image>: the digest, or nothing
  local i
  for i in ${names[@]+"${!names[@]}"}; do
    if [[ "${names[$i]}" == "$1" ]]; then printf '%s' "${digests[$i]}"; return 0; fi
  done
}
for pair in ${image_args[@]+"${image_args[@]}"}; do
  name=${pair%%=*}
  value=${pair#*=}
  [[ "$pair" == *=* && "$name" =~ ^[a-z0-9-]+$ ]] || fail "--image expects name=sha256:<hex>, got '$pair'"
  [[ "$value" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "image $name has no usable index digest: '$value'"
  [[ -z "$(digest_of "$name")" ]] || fail "image $name is given twice"
  names+=("$name")
  digests+=("$value")
done

source_dir=$root/deploy/oci-free-tier
for path in "$source_dir/compose.yml" "$source_dir/Caddyfile" "$source_dir/.env.example" "$source_dir/cloud-init.yaml" "$root/scripts/run-migrations.sh"; do
  [[ -f "$path" ]] || fail "source file $path is missing"
done
[[ -d "$root/migrations/postgres" ]] || fail "$root/migrations/postgres is missing"

bundle=notarx-compose-$version
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
stage=$workdir/$bundle
mkdir -p "$stage"

# Pin every project image: the default of its ${VARIABLE:-reference} expression is replaced.
compose=$(<"$source_dir/compose.yml")
pinned=()
while IFS= read -r line; do
  [[ "$line" =~ ^[[:space:]]*image:[[:space:]]*\$\{([A-Z_]+):-([^}]+)\}[[:space:]]*$ ]] || fail "image line is not one \${VARIABLE:-reference} expression: $line"
  variable=${BASH_REMATCH[1]}
  reference=${BASH_REMATCH[2]}
  if [[ "$reference" == "$source_registry"/* ]]; then
    image=${reference#"$source_registry"/}
    image=${image%%[:@]*}
    [[ -n "$(digest_of "$image")" ]] || fail "no digest given for $registry/$image (needed by $variable)"
    compose=${compose//"$reference"/"$registry/$image@$(digest_of "$image")"}
    pinned+=("$image")
  elif [[ "$reference" != *@sha256:* ]]; then
    fail "third-party image $reference ($variable) is not pinned by digest in deploy/oci-free-tier/compose.yml"
  fi
done < <(grep -E '^[[:space:]]*image:' <<<"$compose")
[[ "${#pinned[@]}" -gt 0 ]] || fail 'compose.yml references no project image'
for name in ${names[@]+"${names[@]}"}; do
  case " ${pinned[*]} " in
    *" $name "*) ;;
    # The optional scanner image is not part of the Compose file; it is accepted and not pinned.
    *) [[ "$name" == notarx-scanner ]] || fail "image $name is given but compose.yml does not use it" ;;
  esac
done
printf '%s\n' "$compose" >"$stage/compose.yml"
# No image is left that a tag could move.
if grep -E '^[[:space:]]*image:' "$stage/compose.yml" | grep -v '@sha256:[0-9a-f]\{64\}'; then
  fail 'an image of the rendered compose.yml is not pinned by digest'
fi

cp "$source_dir/Caddyfile" "$source_dir/cloud-init.yaml" "$stage/"
# The clone-only settings at the end of .env.example do not apply to the bundle, which has the files.
awk '/^# Running from a repository clone/ { exit } { lines[n++] = $0 } END { while (n > 0 && lines[n - 1] == "") n--; for (i = 0; i < n; i++) print lines[i] }' "$source_dir/.env.example" >"$stage/.env.example"
if grep -E '^[A-Z0-9_]*(PASSWORD|TOKEN|SECRET|_KEY)[A-Z0-9_]*=.+' "$stage/.env.example" >/dev/null; then
  fail '.env.example carries a secret value'
fi
cp "$root/scripts/run-migrations.sh" "$stage/run-migrations.sh"
chmod 0755 "$stage/run-migrations.sh"
mkdir "$stage/migrations"
cp -R "$root/migrations/postgres/." "$stage/migrations/"
chmod -R u+rwX,go+rX,go-w "$stage"
chmod 0644 "$stage/compose.yml" "$stage/Caddyfile" "$stage/.env.example" "$stage/cloud-init.yaml"

{
  printf '# notarx compose bundle %s\n\n' "$version"
  cat <<'README'
This directory is the Docker Compose deployment of notarx for one host (the Oracle Cloud Always
Free reference deployment, ADR 0017). Every image is pinned by digest, so the stack does not change
when a tag moves.

| File | Purpose |
| --- | --- |
| `compose.yml` | PostgreSQL, the one-shot migration, notarx, the console and Caddy. |
| `Caddyfile` | Automatic TLS and routing for `NOTARX_DOMAIN`. |
| `.env.example` | Every setting; copy it to `.env` (mode 0600) and fill the secrets. |
| `cloud-init.yaml` | Installs Docker, mounts the data volume and installs the systemd units. |
| `migrations/`, `run-migrations.sh` | The schema the `migrate` service applies before notarx starts. |

Data directories: `cloud-init.yaml` creates them on the OCI reference VM. On any other Docker host create
them first (`NOTARX_DATA_DIR`, default `/srv/notarx`, relocates them); the notarx container runs as
uid 65532 and needs a private object root:

    sudo mkdir -p /srv/notarx/postgres /srv/notarx/objects
    sudo chown 65532:65532 /srv/notarx/objects
    sudo chmod 0700 /srv/notarx/objects

Start: `install -m 0600 .env.example .env`, edit `.env`, then `docker compose up -d`. Setting an image
variable in `.env` replaces the digest pin of that image.
Runbook: https://github.com/mlops-ca/notarx/blob/main/docs/deployment/oci-free-tier.md
README
  printf '\nImages of this bundle:\n\n'
  for name in $(printf '%s\n' "${pinned[@]}" | LC_ALL=C sort -u); do
    # shellcheck disable=SC2016
    printf -- '- `%s/%s@%s`\n' "$registry" "$name" "$(digest_of "$name")"
  done
} >"$stage/README.md"
chmod 0644 "$stage/README.md"

mkdir -p "$output"
output=$(cd "$output" && pwd)
# Plain member names ("notarx-compose-X/compose.yml"), so tar --strip-components=1 flattens the bundle.
create_tar_gz "$workdir" "$output/$bundle.tar.gz" plain
printf 'wrote %s/%s.tar.gz\n' "$output" "$bundle"
