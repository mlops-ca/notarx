# Object storage configuration

[Documentation index](README.md) · [ADR 0017](adr/0017-runtime-backends-and-oci-free-tier.md)

notarx keeps verified immutable bytes behind the `objectstore.Store` port. The
driver and its settings come from `NOTARX_OBJECT_STORE_*` environment variables.
The former RustFS-prefixed names (under the old `GATEWAY_` prefix and the interim
`NOTARX_` prefix) are not read: startup refuses them and names the replacement
(ADR 0013, no aliases).

## Keys

| Key | Default | Meaning |
| --- | --- | --- |
| `NOTARX_OBJECT_STORE_DRIVER` | `s3` | `s3` or `filesystem`. An unknown value fails startup. |
| `NOTARX_OBJECT_STORE_ENDPOINT` | required for `s3` | Base URL with scheme http or https and no path. |
| `NOTARX_OBJECT_STORE_BUCKET` | required for `s3` | Bucket name. |
| `NOTARX_OBJECT_STORE_REGION` | `us-east-1` | SigV4 signing region. Set the real region for OCI and AWS. |
| `NOTARX_OBJECT_STORE_ACCESS_KEY`, `NOTARX_OBJECT_STORE_SECRET_KEY` | required for `s3` | Static credentials. |
| `NOTARX_OBJECT_STORE_PATH_STYLE` | `true` | Path-style addressing. Use `false` for virtual-hosted services. |
| `NOTARX_OBJECT_STORE_CHECKSUM_MODE` | `when_required` | `when_required` or `when_supported`; maps to the SDK request checksum calculation and response checksum validation. |
| `NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET` | `false` | When `true` the gateway creates a missing bucket at start-up. Otherwise it only checks that the bucket exists. The Compose stack sets `true`; production deployments pre-create the bucket. |
| `NOTARX_OBJECT_STORE_MULTIPART_THRESHOLD` | `268435456` (256 MiB) | Objects larger than this are uploaded in parts through the SDK transfer manager. |
| `NOTARX_OBJECT_STORE_MULTIPART_PART_SIZE` | `16777216` (16 MiB) | Part size, between 5 MiB and 5 GiB, and not larger than the threshold. At 16 MiB the largest object is just under 156 GiB, which keeps the part count below the 10,000-part limit; a larger object is refused with an error that says to raise the part size. |
| `NOTARX_OBJECT_STORE_FS_ROOT` | required for `filesystem` | Absolute directory for the filesystem driver. |
| `NOTARX_OBJECT_STORE_FS_FSYNC` | `true` | Flush files and parent directories on commit. |
| `NOTARX_OBJECT_STORE_FS_RESERVE_BYTES` | `1073741824` | Free space the filesystem driver keeps back. A write that would leave less fails with an insufficient-storage error and readiness fails. `0` disables the check. |

Integrity does not depend on the checksum mode: the gateway computes and
verifies the SHA-256 of every verified object itself. `when_required` removes
the client-side CRC headers and trailers that several S3-compatible services
reject.

## Digest attachment and multipart on S3-compatible services

`SetVerifiedDigest` attaches the verified SHA-256 to an existing object. The
driver tries `CopyObject` with the `REPLACE` metadata directive, which rewrites the
metadata on the service without moving the bytes. At start-up it probes that
with a throw-away object whose key contains a slash (the copy source is the
path-escaped `bucket/key`, which encodes `/` as `%2F`), deletes the probe object,
and logs the chosen mode once (`object store digest mode`). If the service refuses
the copy, or accepts it but ignores the new metadata, the driver switches to the
streaming fallback: it reads the object, re-verifies that its bytes hash to the
digest, and writes it again with the digest attached. That costs extra egress and
is sticky for the life of the process. A refusal that appears only later, at use,
falls back the same way. A probe that cannot run (for example, read-only
credentials) leaves the default, copy, in place.

Objects above the multipart threshold are uploaded with the SDK transfer manager
at the configured part size; the part count stays below 10,000 for every object up
to `9,999 × part size`. The limits a provider enforces are confirmed by the live
OCI run below, not hard-coded.

### Live run against Oracle Cloud Object Storage

The `oci-live` workflow (`.github/workflows/oci-live.yml`) runs the object store
contract, the digest probe and a multipart upload at the default part size against
OCI Object Storage, in both checksum modes (so single-part `aws-chunked` uploads
and multipart uploads are both covered, and keys with slashes go through
`CopyObject`). It is gating for the OCI reference deployment: a `CopyObject` `REPLACE`
failure selects the streaming fallback, and a failure the fallback cannot cover
opens a follow-up ADR for a native driver. It never runs for pull requests or
forks. It needs the repository secrets `OCI_OBJECT_STORE_ENDPOINT`,
`OCI_OBJECT_STORE_REGION`, `OCI_OBJECT_STORE_BUCKET` (a pre-created bucket),
`OCI_OBJECT_STORE_ACCESS_KEY` and `OCI_OBJECT_STORE_SECRET_KEY`; without them it
reports that and succeeds. Every case writes under a throw-away prefix and removes
what it wrote. Locally: `go test -tags ocilive -run '^TestOCI' ./internal/objectstore`.

## Filesystem driver

The `filesystem` driver stores objects in a directory on the node, with no extra
service. It is for a single node only: not for several nodes and not for shared
network filesystems. Deduplication across repositories stays with the database
object table; the driver creates no hard links.

Layout under `NOTARX_OBJECT_STORE_FS_ROOT` (mode 0700, one filesystem so that
`rename` is atomic):

| Path | Content |
| --- | --- |
| `data/<key>` | object bytes |
| `meta/<key>.sha256` | verified digest (`sha256:<hex>`) |
| `tmp/` | in-flight writes |
| `.notarx-root` | random root ID |

Keys follow the usual object key rules and also may not contain `..`, empty
segments, segments that start with a dot, control characters or backslashes, and
may not begin with the reserved top-level segments `tmp` or `meta`. Symbolic
links are never followed. A key cannot also be the directory part of another key.

A write streams into `tmp/`, hashes while writing, compares the size and the
expected SHA-256, flushes the file, renames it into `data/`, flushes the parent
directory and then writes the digest file the same way. An overwrite removes the
old digest first, so bytes and digest can never disagree after a crash. A crash
leaves the old object, the new object, or a stray `tmp/` file that start-up
removes once it is older than one hour. An object without a digest file reads as
unverified. Writes to one key are last-writer-wins.

### Single-node identity

On first start the gateway records the driver and the root ID in the
`object_store_identity` table. A gateway whose root ID differs (a second node, a
restored volume, a wrong mount) refuses to start. The gateway also holds a
PostgreSQL session advisory lock keyed on the root ID, so a second process cannot
share the root, and it stops if it loses that lock. To move to a new volume,
restore the database and the object root from the same backup.

### Capacity

The gateway exposes free space on the readiness probe and as metrics:
`notarx_object_store_total_bytes`, `notarx_object_store_free_bytes`,
`notarx_object_store_reserve_bytes` and `notarx_object_store_below_reserve`.
Readiness fails, and writes fail with `objectstore.ErrInsufficientStorage`, while
free space is under the reserve.

## Integration tests

Integration tests read `TEST_OBJECT_STORE_ENDPOINT`, `TEST_OBJECT_STORE_ACCESS_KEY`
and `TEST_OBJECT_STORE_SECRET_KEY`.

## Oracle Cloud Infrastructure Object Storage

Use the S3 compatibility endpoint
`https://<namespace>.compat.objectstorage.<region>.oci.customer-oci.com`, the
tenancy region identifier (for example `ca-toronto-1`) as `REGION`,
`PATH_STYLE=true`, `CHECKSUM_MODE=when_required` and a Customer Secret Key
pair. Pre-create the bucket and leave `AUTO_CREATE_BUCKET=false`. Provider
terms change; confirm limits on the provider's documentation before relying
on them.
