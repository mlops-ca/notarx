# Deploy on Oracle Cloud Always Free (reference deployment)

[Documentation index](../README.md)

This runbook installs notarx on one Oracle Cloud Infrastructure (OCI) Always Free
Ampere A1 virtual machine with Docker Compose. It is a reference deployment, not
a guarantee: provider terms, capacity and reclamation can change, and every OCI
fact below carries the date it was last checked. The design is
[ADR 0017](../adr/0017-runtime-backends-and-oci-free-tier.md), section "Reference
deployment".

The stack, in `deploy/oci-free-tier/`:

- `postgres` (PostgreSQL 18, started with
  `shared_preload_libraries=pg_stat_statements`), `migrate` (the migration
  runner), `notarx`, `notarx-console` and `caddy` (automatic TLS for your DNS
  name). The Compose project is named `notarx`.
- The filesystem object driver is the primary object store:
  `NOTARX_OBJECT_STORE_FS_ROOT=/srv/notarx/objects`. OCI Object Storage only holds
  backups.
- Memory limits: PostgreSQL 3 GB, notarx 3 GB, console 256 MB, Caddy 128 MB and the
  one-shot migration 256 MB. They sum to about 6.6 GB of the 12 GB, leaving about
  5 GB for the operating system and the page cache.
- Only ports 80 and 443 are published. PostgreSQL is on an internal Compose network
  with no route out of the host; notarx and Caddy share the front network.
- The vulnerability scanner (ADR 0030) is off and is not part of this stack.

## OCI Always Free facts

Checked on 2026-11-18 against Oracle's published Always Free resources. Confirm them
on Oracle's current page before you rely on them; Oracle can change them.

| Fact | Value used here |
| --- | --- |
| Compute | Ampere A1 Flex, arm64. The allowance is published as 3,000 OCPU-hours and 18,000 GB-hours per month. This reference runs one instance of 2 OCPU and 12 GB. |
| Block storage | 200 GB in total across boot and block volumes, with a 50 GB minimum boot volume. This deployment uses a 50 GB boot volume and one data volume that fills the rest of the budget. |
| Outbound data | 10 TB per month. |
| Object Storage | 20 GB and 50,000 API requests per month in the Always Free tier. Backups use large `fs-bytes-v1` segments (256 MiB by default), so the request count scales with size divided by segment size, not with the number of objects. |
| Free-tier compute capacity | Not guaranteed. |

**Idle reclamation.** Oracle can reclaim an Always Free instance when, over seven
days, CPU, network and memory utilization are all under 20%. A lightly used
artifact repository can meet that. A Pay As You Go account is not subject to
reclamation of its instances; upgrading the account (it stays free below the
allowance) is the usual remedy. Keep a recent backup either way.

**A1 capacity.** New A1 instances often fail with "Out of host capacity" in a
popular region. Retry later or try another availability domain; capacity is the most
common reason the free tier does not work out. The home region of the tenancy cannot be changed after sign-up.

## Directory layout

`deploy/oci-free-tier/` is what `scripts/render-compose-bundle.sh` (ADR 0025
slice 3) consumes to build the digest-pinned `notarx-compose-<version>.tar.gz`
release asset. Its test (`scripts/render-compose-bundle-test.sh`) checks that this section
names every entry of the directory and of the bundle.

| Path | Purpose |
| --- | --- |
| `compose.yml` | The stack. Every `image:` is one `${VARIABLE:-reference}` expression: `NOTARX_IMAGE`, `NOTARX_CONSOLE_IMAGE`, `NOTARX_POSTGRES_IMAGE` and `NOTARX_CADDY_IMAGE`. The renderer replaces each reference with a digest that covers both architectures. |
| `Caddyfile` | Automatic TLS and routing for `NOTARX_DOMAIN`. |
| `.env.example` | Every setting with an empty value for each secret. Copy it to `.env` (mode 0600). No file in the directory holds a secret. |
| `cloud-init.yaml` | Installs Docker, formats and mounts the data volume, installs the systemd units and enables unattended security upgrades. |
| `tofu/` | Optional OpenTofu module (see "Provision with OpenTofu"). |
| `migrations/`, `run-migrations.sh` | Not in the repository directory. The bundle adds them next to `compose.yml` (copied from `migrations/` and `scripts/run-migrations.sh`). In a clone, point `NOTARX_MIGRATIONS_DIR` and `NOTARX_MIGRATION_RUNNER` at them (see `.env.example`). |

`scripts/oci-free-tier-check.py` (run by `make oci-free-tier-check`) enforces the
rules above in CI: image expressions, no build sections, memory limits, ports,
the PostgreSQL command and the absence of secrets.

## Provision

Two equivalent routes create the OCI resources. Use the console steps or the
OpenTofu module; do not mix them for one deployment.

### Console steps

1. **Compartment.** Create a compartment for the deployment, or use an existing one.
2. **Network.** Create a VCN with a public subnet and an internet gateway. Edit the
   subnet's security list so it allows ingress TCP 22 from your own CIDR only, TCP 80
   and TCP 443 from `0.0.0.0/0`, and all egress.
3. **Instance.** Create a VM.Standard.A1.Flex instance with 2 OCPU and 12 GB, the
   Ubuntu 24.04 arm64 image, the public subnet, a public IP and your SSH key. Paste
   `deploy/oci-free-tier/cloud-init.yaml` into "Cloud-init script".
4. **Data volume.** Create a block volume sized to the rest of the free budget (for
   example 150 GB beside a 50 GB boot volume) and attach it to the instance with the
   default attachment type. Cloud-init formats it once, labels it `notarx` and
   mounts it at `/srv/notarx`.
5. **Backup bucket.** Create a private Object Storage bucket for backups in the
   same region.
6. **Backup identity.** Create an IAM user and a group for it, then a policy scoped
   to one compartment and one bucket, for example
   `Allow group notarx-backup to manage objects in compartment <name> where target.bucket.name='<bucket>'`.
   Create a customer secret key for the user; the secret is shown once. It is the
   S3-compatible access key pair for the bucket.
7. **DNS.** Point an A record for your DNS name at the instance's public IP.

### Provision with OpenTofu

The optional module in `deploy/oci-free-tier/tofu` creates the same resources: VCN,
subnet, security list (22 from one CIDR, 80, 443), A1 instance, block volume and
attachment, Object Storage bucket, IAM user, a customer secret key (a sensitive
output) and a least-privilege policy scoped to one compartment and bucket. See
`deploy/oci-free-tier/tofu/README.md` for variables and the state warning below.

**State holds a secret.** The customer secret key is stored in the OpenTofu state.
Use a remote, encrypted backend with restricted access (for example an OCI Object
Storage bucket with a customer-managed key, or any backend that encrypts at rest),
never commit state, and treat the state file like the secret itself. The
repository ships no state, no `.tfvars` with values and no keys.

## Install

### From the release bundle

The bundle `notarx-compose-<version>.tar.gz` is attached to every stable release (not to
nightlies; ADR 0025 slice 3). It unpacks into one directory holding `compose.yml`, `Caddyfile`,
`.env.example`, `cloud-init.yaml`, `migrations/`, `run-migrations.sh` and a README, with every
image pinned by the digest of the multi-architecture index (amd64 and arm64) built by that
release run. Verify it before you unpack it (the [install guide](../install.md) lists every
channel):

```sh
version=<release version>
base=https://github.com/mlops-ca/notarx/releases/download/v$version
curl -fsSLO "$base/notarx-compose-$version.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify "notarx-compose-$version.tar.gz" --repo mlops-ca/notarx
sudo mkdir -p /opt/notarx
sudo tar -xzf "notarx-compose-$version.tar.gz" -C /opt/notarx --strip-components=1
```

Package signing is deferred (ADR 0025), so the checksums and the build attestation
are the verification. The `.deb` and `.rpm` packages of ADR 0025 are the alternative for a host
that already runs PostgreSQL and a reverse proxy: they run notarx directly against that
database instead of through Compose, and the migration is a manual `notarx-migrate` step
(see the [install guide](../install.md#debian-ubuntu-and-fedora-rhel-packages)).

### From a clone (unreleased code)

Before the first stable release, or to run a commit that is not released, install from a clone.
This path uses the reference tags in `compose.yml` (`:main` for the project images) instead of
digests, so it is not reproducible: use the bundle for anything that matters.

```sh
sudo git clone https://github.com/mlops-ca/notarx /opt/notarx-src
sudo mkdir -p /opt/notarx
sudo cp /opt/notarx-src/deploy/oci-free-tier/{compose.yml,Caddyfile,.env.example} /opt/notarx/
sudo cp -r /opt/notarx-src/migrations /opt/notarx/migrations
sudo cp /opt/notarx-src/scripts/run-migrations.sh /opt/notarx/run-migrations.sh
```

### Configure and start

```sh
cd /opt/notarx
sudo install -m 0600 .env.example .env
sudo vi .env     # set NOTARX_DOMAIN, NOTARX_ACME_EMAIL and every secret (openssl rand -hex 32)
sudo systemctl start notarx.service
docker compose ps
```

Secrets come only from `.env` (mode 0600, outside any YAML file you commit). The
unit `notarx.service` starts only when `/opt/notarx/.env` exists. The first start
pulls images, runs the migration and lets Caddy obtain its certificate; allow a few
minutes. Then open `https://<your DNS name>/` for the Console and use
`NOTARX_ADMIN_TOKEN` for administration.

### Smoke test

`make oci-free-tier-smoke` starts this stack with temporary credentials on a
Docker host (Caddy serves `localhost` with its local CA) and pushes and pulls an
OCI image and a Raw file through Caddy. CI runs it on a native arm64 runner. To check
a running deployment instead, set `NOTARX_SMOKE_URL=https://<your DNS name>` and
`NOTARX_ADMIN_TOKEN`.

## Backups and restore

**What to back up.** The PostgreSQL database and the object root, together: the
database records the identity of the object root, and a root restored without its
database (or the reverse) is refused at start-up.

**Nightly database dump.** Cloud-init installs `notarx-backup.timer`, which writes
`/srv/notarx/backups/notarx-<UTC time>.dump` (custom format) at about 02:30 and keeps
seven days. This protects against database damage on the same volume only.

**Off-VM copy with the backup tooling.** Export the whole installation, database
and objects, as a verified bundle with the offline transfer profile described in
the [recovery runbook](../recovery-runbook.md), then copy the bundle to the bucket.
Stop the notarx first (`docker compose stop notarx`), write a private source spec
with a `filesystem` object source for `/srv/notarx/objects`, and run:

```sh
notarx backup export --spec /private/source.json --bundle /srv/notarx/backups/bundle-$(date -u +%Y%m%d)
docker compose start notarx
```

The objects are exported in the `fs-bytes-v1` format: tar segments (256 MiB by
default) with an inventory of per-object SHA-256 digests, so a nightly upload is a
handful of large objects. Upload the bundle to the backup bucket through its
S3-compatible endpoint (`https://<namespace>.compat.objectstorage.<region>.oraclecloud.com`)
with any S3 client using the customer secret key, for example
`aws s3 sync --endpoint-url <endpoint> <bundle> s3://<bucket>/<date>/`. Restrict the
key to that bucket (see "Console steps").

**Restore drill.** Run it on a spare VM or a throwaway Compose project, never over
production:

1. Install the stack (above) without starting the gateway, and download and verify the
   bundle from the bucket.
2. Restore the database: `docker compose up -d postgres`, then
   `docker compose exec -T postgres pg_restore -U notarx -d notarx --exit-on-error < database.dump`.
3. Restore the objects into a new root and carry the recorded root ID with them:
   `notarx backup restore-objects --bundle <dir> --root /srv/notarx/objects` (the root must be new or empty; the command creates it with the identity from the bundle).
   Make sure the directory is owned by uid 65532 with mode 0700.
4. Start the stack. The gateway starts only when the restored database's object store
   identity matches the restored root; then run `make oci-free-tier-smoke` against it.

The repository's drills (`scripts/backup-drill.sh`, `scripts/restore-drill.sh` with
`NOTARX_OBJECT_STORE_DRIVER=filesystem`, and the filesystem leg of
`make backup-restore-readiness`) exercise the same round trip on every change.

## Variants

**Variant B (documented, not the default): two VMs.** One VM runs PostgreSQL and a
networked S3 backend, which requires `driver=s3` (`NOTARX_OBJECT_STORE_DRIVER=s3`,
see [object storage](../object-storage.md)). Two VMs do not fit the free A1
allowance usefully (two 1 OCPU / 6 GB instances), so this variant needs a Pay As
You Go account; that account is not subject to idle reclamation. The S3 backend is
RustFS, the same server the repository's Compose stack runs. The pinned digest
`rustfs/rustfs:1.0.0-beta.12@sha256:41fe8938...` publishes a linux/arm64 manifest
(checked 2026-11-18, and re-asserted by `make oci-free-tier-images-check`); if a future
pin ever loses arm64, re-pin to a stable multi-arch RustFS release rather than add a
second S3 server.

**Variant C (milestone MySQL HeatWave, ADR 0022).** The same A1 VM with a MySQL
HeatWave Always Free DB system on a private endpoint instead of the PostgreSQL
container. It is not available yet.

## Security notes

- Secrets live in `/opt/notarx/.env` (mode 0600) and nowhere in YAML, the OpenTofu
  module's code or its examples. The state of the optional module holds the customer
  secret key, so use remote encrypted state.
- The OCI security list opens 22 (restricted to one CIDR), 80 and 443. PostgreSQL and
  notarx have no published ports; `/metrics` is not routed by Caddy.
- The object root is mode 0700 and owned by the gateway image's nonroot user
  (uid 65532).
- Caddy terminates TLS. The route list in the `Caddyfile` mirrors the Console's
  proxy rules; add a path there if you enable a format that is not routed yet.

## Operations

- **Upgrade.** Unpack the new bundle over `/opt/notarx` (keep `.env`) and run
  `sudo systemctl restart notarx.service`. The migration runs before notarx starts.
- **Logs.** `docker compose logs --tail=200 notarx`; container logs are capped at
  five 10 MB files.
- **Disk.** Watch `/srv/notarx` (`df -h /srv/notarx`). The filesystem driver keeps a
  1 GiB reserve free by default and refuses writes below it.
