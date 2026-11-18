# Package signing key ceremony and environments

[Documentation index](../README.md)

Status: runbook for ADR 0025 slice 4 (`docs/adr/0025-distribution-channels.md`) and
the S2 amendment of ADR 0026 (D15). English only (ADR 0014).

This runbook creates the OpenPGP key that signs the native apt and yum repositories at
`packages.mlops.ca`, protects the secrets that use it, and records how it is rotated and
revoked. Nothing in the release pipeline may sign or publish before every step in
[Ceremony checklist](#ceremony-checklist) is done.

## Key layout

One certificate, user ID `notarx packages <packages@mlops.ca>`:

| Key | Algorithm | Capability | Validity | Held in |
| --- | --- | --- | --- | --- |
| Primary | RSA 4096 | certify only | no expiry | offline: hardware token plus one offline encrypted backup |
| S1 | RSA 4096 | sign | 2 years | GitHub environment `packages-signing` (armored secret subkey) |
| S2 | RSA 4096 | sign | 2 years | a KMS or HSM behind `notarx-signer` (ADR 0026), used by the notarx-hosted feeds |

Rules that follow from ADR 0025 and ADR 0026:

- RSA, not Ed25519: EL8-era `rpm` and `gnupg` cannot verify EdDSA.
- The primary never leaves offline custody and is never an Actions secret.
- S1 and S2 are never in the same place. Neither is the primary.
- `gpg.key` (also served as `notarx.asc`, identical bytes) publishes the primary and both
  subkeys, so clients can switch between R2 and the feeds without a key change.
- The key is distinct from the reference APT signer fixture key and from customer signers.
  The fixture key signs test repositories only and must never be reused here.

## Prerequisites

1. The monitored alias `packages@mlops.ca` exists and forwards to the owner or team. Send a
   test message and confirm it arrives **before** the key is minted. The user ID is never
   changed afterwards, so this check is a hard gate.
2. An air-gapped or freshly installed machine, GnuPG 2.2 or newer, a hardware token
   (OpenPGP card or equivalent) and two offline encrypted media in separate locations.
3. Two maintainers present (four-eyes), one of whom is not the key holder.
4. Rehearse the whole procedure first with `make packages-signing-drill`, which runs it on
   a throwaway staging key (see [Drill](#revocation-and-rotation-drill)).

## Create the primary key

Use a clean `GNUPGHOME` on the offline machine:

```sh
export GNUPGHOME=$(mktemp -d) && chmod 700 "$GNUPGHOME"
gpg --batch --pinentry-mode loopback --passphrase-fd 0 --quick-generate-key \
  'notarx packages <packages@mlops.ca>' rsa4096 cert never <<< "$PASSPHRASE"
FPR=$(gpg --list-keys --with-colons 'packages@mlops.ca' | awk -F: '/^fpr/ {print $10; exit}')
```

Use a long random passphrase generated for this purpose and split custody of it (for
example, one half per maintainer in separate sealed envelopes).

## Add signing subkey S1

```sh
gpg --batch --pinentry-mode loopback --passphrase-fd 0 --quick-add-key "$FPR" rsa4096 sign 2y <<< "$PASSPHRASE"
S1=$(gpg --list-keys --with-colons "$FPR" | awk -F: '/^sub:/ {want = 1; next} /^fpr/ && want {print $10; want = 0}' | tail -n 1)
```

Export only the secret S1 for the `packages-signing` environment. The trailing `!` selects exactly that
subkey, so S2 (or any later subkey) is never included, and the primary secret is stripped; confirm with `gpg --list-secret-keys` after importing it
into a scratch keyring that the primary shows `sec#` (secret part absent).

```sh
gpg --batch --pinentry-mode loopback --passphrase-fd 0 --armor \
  --export-secret-subkeys "$S1!" <<< "$PASSPHRASE" > s1-secret.asc
```

Store the S1 passphrase-less or passphrase-protected armor as the Actions secret
`PACKAGES_SIGNING_SUBKEY` (and its passphrase as `PACKAGES_SIGNING_PASSPHRASE` if protected)
in the `packages-signing` environment only, then shred `s1-secret.asc`.

## Certify the second signing subkey S2

S2 is created for the notarx-hosted feeds (ADR 0026) and certified by the same offline
primary, so the primary never has to be online or exposed to the KMS.

1. Create the S2 key material inside the KMS or HSM as the ADR 0026 slice 3 live run
   decided (HSM-protected OCI Vault RSA 4096 first, software-protected second, SoftHSM2
   fallback with its weaker custody claim stated in `docs/release/feeds-signing.md`).
2. With the primary available on the offline machine, run `notarx-signer cert-init`. It reads
   the S2 public key from the KMS and emits the public OpenPGP certificate in which the
   offline primary has produced the subkey binding signature for S2 (sign capability,
   two-year expiry, embedded primary-key binding signature made through the KMS).
3. Check the result: `gpg --show-keys --with-colons cert.asc` lists the primary with
   capability `c` and two subkeys `s` with expiry two years out.
4. Store the resulting certificate as the signer's `_CERT_FILE`. The KMS holds the only
   copy of the S2 private key; no armored S2 secret exists.

ADR 0026 slice 3 performs this step against the live KMS and records the result; until it
lands, publish `gpg.key` with the primary and S1 only and republish when S2 exists.

## Revocation certificate

Generate it immediately after creating the primary, while it is still offline:

```sh
# Interactive: confirm, choose reason 0 (no reason specified), enter a description, confirm,
# then type the primary passphrase at the pinentry prompt.
gpg --output revoke.asc --gen-revoke "$FPR"
```

GnuPG 2.1 and newer also writes a revocation certificate to `openpgp-revocs.d/<fingerprint>.rev` in
`GNUPGHOME` at key creation; move it to the offline media and delete it from the machine. The
explicit `--gen-revoke` run above produces a certificate with a recorded reason and is the one to keep.

The revocation certificate is stored offline on separate media from the primary backup,
in a different physical location. Anyone holding it can revoke the key, so treat it like
the primary. Test that it parses (`gpg --show-keys revoke.asc`) but never import it into the
production keyring.

## Custody

| Item | Location | Access |
| --- | --- | --- |
| Primary secret key | hardware token plus one encrypted offline backup | two maintainers, ceremony only |
| Primary passphrase | split, sealed, separate locations | two maintainers |
| Revocation certificate | offline media, separate from the primary backup | two maintainers |
| S1 secret subkey | `packages-signing` environment secret | environment reviewers via protected job |
| S2 | KMS behind `notarx-signer` | signer service identity only |
| Public key | `gpg.key`, `notarx.asc`, `SECURITY.md` fingerprint | public |

Record each custody change (who, when, what) in the maintainers' private log.

## Publish the public key and fingerprint

```sh
gpg --armor --export "$FPR" > gpg.key
cp gpg.key notarx.asc
```

`--export` includes every subkey with its binding signatures, so after S2 is certified a re-export
publishes both. Upload to the R2 bucket root as `/gpg.key` and `/notarx.asc` (same bytes). Record
the full 40-character fingerprint in `SECURITY.md` under "Package signing key", in the release
notes of the first release signed with it, and on the install page. Verify the fingerprint
by an independent channel before announcing it (read it aloud between two maintainers).

## Environments and secrets

ADR 0025 separates signing from publishing:

| Environment | Holds | Must not hold |
| --- | --- | --- |
| `packages-signing` | S1 armored secret subkey (`PACKAGES_SIGNING_SUBKEY`, optional `PACKAGES_SIGNING_PASSPHRASE`) | any Cloudflare credential |
| `packages-publish` | Cloudflare API token limited to the one R2 bucket (`CLOUDFLARE_API_TOKEN`) | any signing key |

Both environments require reviewers, forbid self-review, and restrict deployments to the release
tag pattern `v*` (a deployment tag policy; no branch policy). Configure them in
Settings > Environments, or with `gh api`. `make packages-environments-test` exercises the checker
against stubbed API responses:

```sh
for env in packages-signing packages-publish; do
  gh api -X PUT "repos/mlops-ca/notarx/environments/$env" --input - <<JSON
{"prevent_self_review": true,
 "reviewers": [{"type": "User", "id": <reviewer-user-id>}, {"type": "User", "id": <second-reviewer-id>}],
 "deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
JSON
  gh api -X POST "repos/mlops-ca/notarx/environments/$env/deployment-branch-policies" \
    -f name='v*' -f type=tag
done
```

Self-review prevention needs at least two reviewers so someone other than the person who
triggered the run can approve. Verify the settings at any time (the script is read-only):

```sh
scripts/check-packages-environments.sh            # settings only
scripts/check-packages-environments.sh --require-secrets   # after the ceremony: secrets present and scoped
```

The Cloudflare token is created at Cloudflare with the permission "Workers R2 Storage: Edit"
restricted to the single bucket, never an account-wide token.

## Staging

A staging R2 bucket (`packages-staging`) and a staging key are used for every rehearsal and for
workflow development (ADR 0025 slices 5 and 6). The staging key is created with the same
steps but user ID `notarx packages staging <packages-staging@mlops.ca>`, is never published at
`packages.mlops.ca`, and its secret and the staging Cloudflare token (scoped to the staging bucket
only) live in the same two environments only while a rehearsal runs, or in `packages-staging-*`
environments with identical protections. Staging fingerprints are never recorded in `SECURITY.md`.

## Rotation

- The subkeys expire two years after creation. Calendar reminders at six months before expiry
  start rotation, so there is a long overlap.
- To rotate, bring the primary online on the offline machine, add a new signing subkey (and,
  for S2, run `cert-init` against the new KMS key version), revoke the old subkey after the new
  one is deployed, export and republish `gpg.key`, and announce the new fingerprint list in the
  release notes and docs.
- The primary fingerprint does not change, so clients that already trust the primary through
  `gpg.key` only need to refetch `gpg.key`. Until a `notarx-archive-keyring` package exists, the install
  docs tell users to refetch `gpg.key` (`curl -fsSL https://packages.mlops.ca/gpg.key`).
- Extending expiry of a subkey is also a valid rotation when the key is not suspected compromised,
  but a new subkey is preferred because it exercises the process.

## Revocation

| Event | Action |
| --- | --- |
| S1 leaked or suspected | Revoke subkey S1 with the offline primary, add a new S1, update the environment secret, republish `gpg.key`, re-sign suites with the new subkey |
| S2 leaked or suspected | Revoke subkey S2 with the offline primary, disable the KMS key version, create and certify a new S2, republish `gpg.key`; the R2 path is unaffected |
| Primary lost or compromised | Import `revoke.asc`, publish the revoked `gpg.key`, announce through release notes, SECURITY.md and the install page, then run a new ceremony with a new fingerprint |

Revoking a subkey does not need the revocation certificate, only the primary. The certificate
is the last resort when the primary is lost.

## Revocation and rotation drill

`scripts/packages-signing-drill.sh` (run with `make packages-signing-drill`) rehearses the whole
lifecycle on a throwaway staging key in a temporary `GNUPGHOME`:

1. Create the certify-only RSA 4096 primary, S1 and an S2 stand-in, with two-year expiry.
2. Export only the secret S1 to a second keyring and confirm the primary is not there (`sec#`).
3. Sign a Release-like document with S1 and verify it against the exported public key.
4. Rotate: add S1', revoke S1 with the primary, re-export the public key, and confirm a
   verifier with the new `gpg.key` rejects a signature made earlier by S1 (revoked) and accepts
   a signature from S1'.
5. Revoke the primary with the revocation certificate in a verifier keyring and confirm the
   certificate shows as revoked and signatures from it are no longer accepted.

The drill must pass before the production ceremony and again after any change to this runbook.
The result of the run that accompanied the slice is linked from the pull request.

## Ceremony checklist

- [ ] `packages@mlops.ca` exists, forwards, and a test message was received.
- [ ] `make packages-signing-drill` passed on the machine's GnuPG version.
- [ ] Primary created offline; passphrase split; backup made on two media.
- [ ] Revocation certificate generated, parsed and stored offline separately.
- [ ] S1 created, exported as secret subkey only, stored in `packages-signing`, local copy shredded.
- [ ] S2 certified by the offline primary (ADR 0026 slice 3) or explicitly deferred.
- [ ] `gpg.key` and `notarx.asc` published with all subkeys; byte-identical.
- [ ] Fingerprint verified out of band and recorded in `SECURITY.md`.
- [ ] `scripts/check-packages-environments.sh --require-secrets` passes.
- [ ] Rotation reminder created for six months before subkey expiry.
