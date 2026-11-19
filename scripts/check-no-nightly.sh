#!/usr/bin/env bash
# Asserts that no stable release artifact carries the string "nightly" (ADR 0024, version
# identity): the stable build is a rebuild of the verified commit stamped with the clean X.Y.Z,
# so every surface must report it.
#
#   scripts/check-no-nightly.sh --dir <release assets> [--manifest release-manifest.json --registry ghcr.io/owner]
#
# Checked in <dir>: every file name (release-manifest.json is exempt: it records the verified
# nightly on purpose), SHA256SUMS, every archive member name, VERSION.txt of every archive and
# every extracted file (the binaries) for a stamped nightly version (X.Y.Z-nightly.N; the bare
# word is not searched there because a dependency may contain it). The .deb and .rpm packages
# are checked by name; their payload equals the archive binaries (scripts/package-check.sh).
# With --manifest, the labels of every image platform recorded in the manifest are read from
# the registry and must not contain it either. DOCKER overrides the docker binary (tests).
set -euo pipefail

fail() {
  printf 'check-no-nightly: %s\n' "$1" >&2
  exit 1
}

dir=''
manifest=''
registry=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --dir) dir=$2 ;;
    --manifest) manifest=$2 ;;
    --registry) registry=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done
[[ -d "$dir" ]] || fail "--dir '$dir' is not a directory"
[[ -z "$manifest" || -n "$registry" ]] || fail '--manifest needs --registry'

stamped='[0-9]+\.[0-9]+\.[0-9]+-nightly\.[0-9]+'
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
checked=0

while IFS= read -r path; do
  name=$(basename "$path")
  [[ "$name" != release-manifest.json ]] || continue
  checked=$((checked + 1))
  if grep -q -i nightly <<<"$name"; then fail "asset name '$name' contains nightly"; fi
  case $name in
    SHA256SUMS)
      if grep -q -i nightly "$path"; then fail 'SHA256SUMS contains nightly'; fi
      ;;
    *.tar.gz)
      if tar -tzf "$path" | grep -q -i nightly; then fail "$name holds a member whose name contains nightly"; fi
      extract=$scratch/$name.d
      mkdir -p "$extract"
      tar -xzf "$path" -C "$extract"
      while IFS= read -r file; do
        if [[ "$(basename "$file")" == VERSION.txt ]] && grep -q -i nightly "$file"; then
          fail "$name: VERSION.txt reports a nightly version"
        fi
        if grep -a -q -E "$stamped" "$file"; then fail "$name: $(basename "$file") embeds a stamped nightly version"; fi
      done < <(find "$extract" -type f)
      rm -rf "$extract"
      ;;
  esac
done < <(find "$dir" -maxdepth 1 -type f | sort)
[[ "$checked" -gt 0 ]] || fail "no asset found in $dir"

images=0
if [[ -n "$manifest" ]]; then
  docker_cli=${DOCKER:-docker}
  while IFS=$'\t' read -r image digest; do
    labels=$("$docker_cli" buildx imagetools inspect "$registry/$image@$digest" --format '{{ json .Image }}') || fail "cannot inspect $image@$digest"
    values=$(jq -r '[.. | .Labels? // empty | objects | .[]] | .[]' <<<"$labels") || fail "unreadable image config of $image"
    if grep -q -i nightly <<<"$values"; then fail "a label of image $image contains nightly"; fi
    images=$((images + 1))
  done < <(jq -r '.images | to_entries[] | [.key, .value] | @tsv' "$manifest")
  [[ "$images" -gt 0 ]] || fail "the manifest lists no image"
fi
printf 'check-no-nightly: %d asset(s) and %d image(s) carry no nightly version\n' "$checked" "$images"
