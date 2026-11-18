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
| `NOTARX_OBJECT_STORE_FS_ROOT` | required for `filesystem` | Absolute directory for the filesystem driver. |
| `NOTARX_OBJECT_STORE_FS_FSYNC` | `true` | Flush files and parent directories on commit. |

Integrity does not depend on the checksum mode: the gateway computes and
verifies the SHA-256 of every verified object itself. `when_required` removes
the client-side CRC headers and trailers that several S3-compatible services
reject.

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
