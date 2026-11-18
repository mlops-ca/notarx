#!/usr/bin/env bash
# Rehearses the package signing key lifecycle on a throwaway staging key
# (ADR 0025 slice 4, docs/release/packages-signing.md): creation, secret
# subkey export without the primary, signing, subkey rotation, and primary
# revocation. Never touches a real keyring; everything lives in a temp dir.
set -euo pipefail

command -v gpg >/dev/null 2>&1 || { printf 'missing required tool gpg\n' >&2; exit 2; }

work=$(mktemp -d)
trap 'gpgconf --homedir "$work/offline" --kill all >/dev/null 2>&1 || true; gpgconf --homedir "$work/signer" --kill all >/dev/null 2>&1 || true; gpgconf --homedir "$work/client" --kill all >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
pass=drill-passphrase-not-a-secret
uid='notarx packages staging <packages-staging@mlops.ca>'

step() { printf '== %s\n' "$*"; }
die() { printf 'DRILL FAILED: %s\n' "$*" >&2; exit 1; }

newhome() { mkdir -m 700 "$work/$1"; }
resethome() { gpgconf --homedir "$work/$1" --kill all >/dev/null 2>&1 || true; rm -rf "${work:?}/$1"; newhome "$1"; }
g() { local home=$1; shift; gpg --homedir "$work/$home" --batch --yes --no-tty --pinentry-mode loopback --passphrase "$pass" "$@"; }
# Succeeds only for a good signature by a key that is neither revoked nor expired.
trusted_verify() {
  local status
  status=$(gpg --homedir "$work/client" --batch --no-tty --status-fd 1 --verify "$1" "$2" 2>/dev/null) || return 1
  grep -q '^\[GNUPG:\] VALIDSIG' <<<"$status" && ! grep -Eq '^\[GNUPG:\] (REVKEYSIG|EXPKEYSIG|EXPSIG|BADSIG|ERRSIG)' <<<"$status"
}
colons() { local home=$1; shift; gpg --homedir "$work/$home" --batch --no-tty --with-colons "$@" 2>/dev/null; }

newhome offline; newhome signer; newhome client

step "create certify-only RSA 4096 primary, S1 and S2 stand-in (2 year subkeys)"
g offline --quick-generate-key "$uid" rsa4096 cert never 2>/dev/null
fpr=$(colons offline --list-keys | awk -F: '/^fpr/ {print $10; exit}')
[[ ${#fpr} -eq 40 ]] || die "primary fingerprint not found"
g offline --quick-add-key "$fpr" rsa4096 sign 2y 2>/dev/null
g offline --quick-add-key "$fpr" rsa4096 sign 2y 2>/dev/null
[[ $(colons offline --list-keys | grep -c '^sub:') -eq 2 ]] || die "expected two subkeys"
[[ $(colons offline --list-keys | awk -F: '/^pub:/ {print $12}') == *c* ]] || die "primary lacks certify capability"
if colons offline --list-keys | awk -F: '/^pub:/ {print $12}' | grep -q '[sea]'; then
  die "primary must be certify-only"
fi
[[ $(colons offline --list-keys | awk -F: '/^pub:/ {print $3}') == 4096 ]] || die "primary is not RSA 4096"

step "generate revocation certificate"
printf 'y\n0\ndrill\n\ny\n' | gpg --homedir "$work/offline" --no-tty --pinentry-mode loopback --passphrase "$pass" --command-fd 0 --output "$work/revoke.asc" --gen-revoke "$fpr" >/dev/null 2>&1 || die "gen-revoke failed"

step "export secret S1 only and confirm the primary secret is absent"
subkeys() { colons "$1" --list-keys | awk -F: '/^sub:/ {want = 1; next} /^fpr/ && want {print $10; want = 0}'; }
s1=$(subkeys offline | sed -n 1p)
[[ -n "$s1" ]] || die "S1 fingerprint not found"
g offline --armor --export-secret-subkeys "$s1!" >"$work/s1-secret.asc" 2>/dev/null
g signer --import "$work/s1-secret.asc" >/dev/null 2>&1
colons signer --list-secret-keys | awk -F: '/^sec:/ {found = 1; if ($15 == "#") stub = 1} END {exit !(found && stub)}' || die "primary secret key is present in the signing keyring"
colons signer --list-secret-keys | grep -q '^ssb:' || die "S1 secret subkey missing in the signing keyring"

step "sign a Release-like document with S1 and verify with the published key"
g offline --armor --export "$fpr" >"$work/gpg.key" 2>/dev/null
printf 'Origin: notarx\nSuite: stable\nDate: drill\n' >"$work/Release"
g signer --armor --detach-sign --local-user "$s1!" --output "$work/Release.gpg" "$work/Release" 2>/dev/null
g client --import "$work/gpg.key" >/dev/null 2>&1
trusted_verify "$work/Release.gpg" "$work/Release" || die "signature from S1 did not verify"
printf 'tampered\n' >>"$work/Release"
if trusted_verify "$work/Release.gpg" "$work/Release"; then die "tampered document verified"; fi
head -n 3 "$work/Release" >"$work/Release.tmp" && mv "$work/Release.tmp" "$work/Release"
cp "$work/Release.gpg" "$work/Release.s1.gpg"

step "rotate: add S1', revoke S1 with the offline primary, republish"
g offline --quick-add-key "$fpr" rsa4096 sign 2y 2>/dev/null
s1new=$(subkeys offline | sed -n 3p)
[[ -n "$s1new" && "$s1new" != "$s1" ]] || die "new subkey not found"
g offline --command-fd 0 --edit-key "$fpr" >/dev/null 2>&1 <<EOF || die "subkey revocation failed"
key $s1
revkey
y
0
drill rotation

y
save
EOF
g offline --armor --export "$fpr" >"$work/gpg-rotated.key" 2>/dev/null
resethome client
g client --import "$work/gpg-rotated.key" >/dev/null 2>&1
colons client --list-keys | awk -F: -v f="$s1" '/^sub:/ {state=$2} /^fpr/ && $10 == f {print state}' | grep -q '^r$' || die "S1 is not shown as revoked after rotation"
g offline --armor --export-secret-subkeys "$s1new!" >"$work/s1new-secret.asc" 2>/dev/null
resethome signer
g signer --import "$work/s1new-secret.asc" >/dev/null 2>&1
g signer --armor --detach-sign --local-user "$s1new!" --output "$work/Release.gpg" "$work/Release" 2>/dev/null
trusted_verify "$work/Release.gpg" "$work/Release" || die "signature from the rotated subkey did not verify"
# A signature S1 made before the rotation no longer counts once the revocation is imported.
if trusted_verify "$work/Release.s1.gpg" "$work/Release"; then die "signature from the revoked S1 still verifies"; fi

step "revoke the primary with the revocation certificate"
g client --import "$work/revoke.asc" >/dev/null 2>&1 || die "revocation certificate import failed"
colons client --list-keys | awk -F: '/^pub:/ {print $2}' | grep -q '^r$' || die "primary is not shown as revoked"
if trusted_verify "$work/Release.gpg" "$work/Release"; then
  die "signature still verifies after the primary was revoked"
fi

printf 'packages signing drill passed (staging fingerprint %s)\n' "$fpr"
