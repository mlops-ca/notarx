#!/usr/bin/env bash
# Tests scripts/promote-release.sh with a stub gh and docker (ADR 0024 slice 5). Nothing here
# touches the network or the checkout.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

failures=0
cases=0
fail() {
  failures=$((failures + 1))
  printf 'FAIL: %s\n' "$1" >&2
}
expect_ok() {
  local name=$1
  shift
  cases=$((cases + 1))
  "$@" >"$workdir/out" 2>&1 || fail "$name: expected success, got: $(cat "$workdir/out")"
}
expect_fail() {
  local name=$1 pattern=$2
  shift 2
  cases=$((cases + 1))
  if "$@" >"$workdir/out" 2>&1; then
    fail "$name: expected failure"
  elif ! grep -q -E "$pattern" "$workdir/out"; then
    fail "$name: failure message does not match [$pattern]: $(cat "$workdir/out")"
  fi
}
expect_eq() {
  cases=$((cases + 1))
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
}

# --- stubs -------------------------------------------------------------------
stub=$workdir/stub
mkdir -p "$stub/assets"
cat >"$workdir/gh" <<'STUB'
#!/usr/bin/env bash
# Records every call; answers from the files of $STUB.
printf '%s\n' "$*" >>"$STUB/calls"
jq_filter=''
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  if [[ "${args[i]}" == --jq ]]; then jq_filter=${args[i + 1]}; fi
done
emit() {
  if [[ -n "$jq_filter" ]]; then jq -r "$jq_filter" <"$1"; else cat "$1"; fi
}
case "$1 $2" in
  "release view")
    [[ -f "$STUB/view-$3.json" ]] || exit 1
    emit "$STUB/view-$3.json"
    ;;
  "release download")
    tag=$3
    dest=''
    pattern=''
    for ((i = 0; i < ${#args[@]}; i++)); do
      [[ "${args[i]}" == --dir ]] && dest=${args[i + 1]}
      [[ "${args[i]}" == --pattern ]] && pattern=${args[i + 1]}
    done
    [[ -f "$STUB/assets/$tag/$pattern" ]] || exit 1
    cp "$STUB/assets/$tag/$pattern" "$dest/$pattern"
    ;;
  "release list") cat "$STUB/stables" ;;
  "release create" | "release edit" | "release delete") ;;
  "attestation verify")
    if [[ -f "$STUB/attest-fail" ]] && grep -q -F -f "$STUB/attest-fail" <<<"$*"; then exit 1; fi
    ;;
  "api repos/o/r/git/ref/tags/v0.1.0") emit "$STUB/tag-ref.json" ;;
  *) exit 1 ;;
esac
STUB
cat >"$workdir/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB/docker-calls"
STUB
chmod +x "$workdir/gh" "$workdir/docker"
export STUB=$stub GH_CLI=$workdir/gh DOCKER=$workdir/docker

sha=0123456789abcdef0123456789abcdef01234567
other=fedcba9876543210fedcba9876543210fedcba98
digest=sha256:$(printf 'a%.0s' $(seq 64))
nightly=v0.1.0-nightly.2
images='[{"image":"notarx"},{"image":"notarx-console"}]'

reset() {
  rm -rf "$stub"
  mkdir -p "$stub/assets/$nightly"
  : >"$stub/calls"
  : >"$stub/docker-calls"
}
calls() { cat "$stub/calls"; }
promote() { bash "$root/scripts/promote-release.sh" "$@"; }

# --- verify ------------------------------------------------------------------
write_nightly() { # write_nightly <manifest sha>
  printf 'archive' >"$stub/assets/$nightly/notarx_0.1.0-nightly.2_linux_amd64.tar.gz"
  jq -n --arg sha "$1" --arg d "$digest" '{version: "0.1.0-nightly.2", core: "0.1.0", sha: $sha, built_at: "2026-11-19T03:00:00Z",
    images: {notarx: $d}, assets: [{name: "notarx_0.1.0-nightly.2_linux_amd64.tar.gz", sha256: "00"}]}' >"$stub/assets/$nightly/release-manifest.json"
  printf '{"isDraft":false,"isPrerelease":true}\n' >"$stub/view-$nightly.json"
}
verify() { promote verify --repo o/r --nightly-tag "$nightly" --sha "$sha" --dir "$workdir/dl" --attempts 1 --delay 0 "$@"; }

reset
write_nightly "$sha"
expect_ok 'a nightly with a manifest and valid attestations verifies' verify
cases=$((cases + 1))
grep -q 'attestation verify oci://ghcr.io/o/notarx@' "$stub/calls" || fail 'verify did not check the image attestation'
grep -q 'attestation verify .*notarx_0.1.0-nightly.2_linux_amd64.tar.gz' "$stub/calls" || fail 'verify did not check the archive attestation'

reset
write_nightly "$sha"
rm "$stub/assets/$nightly/release-manifest.json"
expect_fail 'a nightly without release-manifest.json is refused' 'no release-manifest.json' verify

reset
write_nightly "$other"
expect_fail 'a manifest for another commit is refused' 'does not record commit' verify

reset
write_nightly "$sha"
printf 'notarx_0.1.0-nightly.2_linux_amd64.tar.gz\n' >"$stub/attest-fail"
expect_fail 'a failing attestation is refused' 'does not verify' verify

reset
write_nightly "$sha"
printf '{"isDraft":true,"isPrerelease":true}\n' >"$stub/view-$nightly.json"
expect_fail 'a draft nightly is refused' 'not a published pre-release' verify

reset
expect_fail 'a missing nightly release is refused' 'does not exist' verify
expect_fail 'a stable tag is not a nightly' 'must be vX.Y.Z-nightly.N' promote verify --repo o/r --nightly-tag v0.1.0 --sha "$sha" --dir "$workdir/dl"

# --- create ------------------------------------------------------------------
assets=$workdir/assets
mkdir -p "$assets"
printf 'a' >"$assets/notarx_0.1.0_linux_amd64.tar.gz"
printf 's' >"$assets/SHA256SUMS"
printf 'm' >"$assets/release-manifest.json"
printf 'ignored' >"$assets/notes.txt"
printf 'notes\n' >"$workdir/notes.md"
create() { promote create --repo o/r --version 0.1.0 --sha "$sha" --notes-file "$workdir/notes.md" --assets-dir "$assets"; }

reset
printf '{"object":{"sha":"%s"}}\n' "$sha" >"$stub/tag-ref.json"
# The first view fails (no release yet) and the tag resolves only after publishing.
expect_ok 'a first run creates a draft, uploads and publishes it' create
cases=$((cases + 1))
grep -q 'release create v0.1.0 .*--draft --target '"$sha" "$stub/calls" || fail 'the release was not created as a draft on the planned commit'
grep -q 'release create v0.1.0 .*notarx_0.1.0_linux_amd64.tar.gz' "$stub/calls" || fail 'the archive was not attached'
grep -q 'release create v0.1.0 .*SHA256SUMS' "$stub/calls" || fail 'SHA256SUMS was not attached'
if grep -q 'notes.txt' "$stub/calls"; then fail 'an unrelated file was attached'; fi
grep -q 'release edit v0.1.0 .*--draft=false' "$stub/calls" || fail 'the draft was not published'

reset
printf '{"isDraft":true}\n' >"$stub/view-v0.1.0.json"
printf '{"object":{"sha":"%s"}}\n' "$sha" >"$stub/tag-ref.json"
expect_ok 'a leftover draft is replaced' create
cases=$((cases + 1))
grep -q 'release delete v0.1.0 .*--yes' "$stub/calls" || fail 'the leftover draft was not deleted'

reset
printf '{"isDraft":false}\n' >"$stub/view-v0.1.0.json"
printf '{"object":{"sha":"%s"}}\n' "$sha" >"$stub/tag-ref.json"
expect_ok 'a published release on the commit converges without writing' create
expect_eq 'no write call on convergence' "$(grep -c -E 'release (create|edit|delete)' "$stub/calls" || true)" 0

reset
printf '{"isDraft":false}\n' >"$stub/view-v0.1.0.json"
printf '{"object":{"sha":"%s"}}\n' "$other" >"$stub/tag-ref.json"
expect_fail 'a published release on another commit is refused' 'another commit' create

reset
printf '{"object":{"sha":"%s"}}\n' "$other" >"$stub/tag-ref.json"
expect_fail 'a tag that does not land on the commit is refused' 'does not point at' create
mkdir "$workdir/none"
expect_fail 'an assets directory without assets is refused' 'no release asset' promote create --repo o/r --version 0.1.0 --sha "$sha" --notes-file "$workdir/notes.md" --assets-dir "$workdir/none"
expect_fail 'a bad version is refused' 'must be X.Y.Z' promote create --repo o/r --version 0.1 --sha "$sha" --notes-file "$workdir/notes.md" --assets-dir "$assets"

# --- finish ------------------------------------------------------------------
finish() { promote finish --repo o/r --version 0.1.0 --nightly-tag "$nightly"; }
reset
printf '{"body":"Original notes."}\n' >"$stub/view-$nightly.json"
expect_ok 'the nightly notes get a pointer to the stable release' finish
cases=$((cases + 1))
grep -q "release edit $nightly " "$stub/calls" || fail 'the nightly notes were not edited'

reset
printf '{"body":"Original.\\n\\n> Promoted to stable v0.1.0: done"}\n' >"$stub/view-$nightly.json"
expect_ok 'a second finish is a no-op' finish
expect_eq 'no edit on the second finish' "$(grep -c 'release edit' "$stub/calls" || true)" 0

# --- latest ------------------------------------------------------------------
latest() { promote latest --repo o/r --version 0.1.0 --registry ghcr.io/o --images-json "$images"; }
reset
printf 'v0.1.0\n' >"$stub/stables"
expect_ok ':latest moves for the newest stable' latest
expect_eq 'one imagetools call per image' "$(grep -c 'buildx imagetools create --tag ghcr.io/o/.*:latest ghcr.io/o/.*:0.1.0' "$stub/docker-calls")" 2

reset
printf 'v0.2.0\nv0.1.0\n' >"$stub/stables"
expect_ok ':latest stays when a newer stable exists' latest
expect_eq 'no imagetools call for an older stable' "$(wc -l <"$stub/docker-calls" | tr -d ' ')" 0

reset
printf 'v0.0.9\n' >"$stub/stables"
expect_fail ':latest refuses to move before the tag exists' 'not published yet' latest
expect_fail 'an unknown command is refused' 'unknown command' promote bogus --repo o/r

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d promote-release case(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'promote-release-test passed (%d cases).\n' "$cases"
