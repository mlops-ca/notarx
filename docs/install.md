# Install notarx

[Documentation index](README.md)

notarx ships through one channel per kind of host. Every channel carries the same stable or
nightly build of one commit; pick the row that fits and follow its section.

| You want | Channel | Section |
| --- | --- | --- |
| The `notarxctl` client on a workstation or CI runner | Homebrew, a `.deb` or `.rpm`, or an archive | [Homebrew](#homebrew), [Packages](#debian-ubuntu-and-fedora-rhel-packages), [Archives](#archives) |
| The server on a host that already runs PostgreSQL | `.deb` or `.rpm` | [Packages](#debian-ubuntu-and-fedora-rhel-packages) |
| The whole stack on one host with Docker | Compose bundle | [Compose bundle](#compose-bundle) |
| Kubernetes or your own orchestration | Container images | [Container images](#container-images) |

Design: [ADR 0025](adr/0025-distribution-channels.md).

## Stable and nightly

A **stable** release (`vX.Y.Z`) is cut by hand from a verified nightly commit. A **nightly**
(`vX.Y.Z-nightly.N`) is built every night from the latest green commit of `main` and is a
GitHub pre-release. Both are built, tested and attested by the same release workflow, and a stable
release is a rebuild of a nightly's commit stamped with the clean version, never a relabelled
nightly.

Install one channel on a host, not both. A nightly always sorts below the stable release it
precedes, so moving from a nightly to the stable release of the same version is a plain upgrade
(`0.2.0-nightly.3` is older than `0.2.0`), and moving back to a nightly needs an explicit
downgrade. Use a stable release for anything that matters. Nightlies are published as release
assets and container tags only: they have no Homebrew formula and no compose bundle.

To switch from a nightly to a stable release, install the stable package or image over it
(`apt install ./notarx_<version>_<arch>.deb` upgrades in place) and run `notarx-migrate` again.
To switch from stable to a nightly, remove the stable packages first, install the nightly files
and run `notarx-migrate`; a downgrade across a schema migration is not supported (restore from
a backup instead, see the [recovery runbook](recovery-runbook.md)).

## Verify a download

Every release asset (archives, packages, the compose bundle) is listed in `SHA256SUMS` and carries
a GitHub artifact attestation (Sigstore, keyless) made by the release workflow of this repository.
Verify either or both before you install:

```sh
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify <file> --repo mlops-ca/notarx
```

OpenPGP signing of packages and an apt or yum repository are deferred ([ADR 0025](adr/0025-distribution-channels.md),
amendment of 2026-11-18): there is no repository to add, no repository key to trust and no key
rotation to follow yet. When signing returns, this page gains the repository snippets and the key
fingerprint, and the release assets stay available.

## Debian, Ubuntu and Fedora, RHEL packages

Three packages are attached to every release, built natively on amd64 and arm64:

| Package | Contents | Architecture |
| --- | --- | --- |
| `notarx` | The server: `/usr/bin/notarx`, a hardened systemd unit, `/etc/notarx/notarx.env`, the migrations and `notarx-migrate`. | amd64, arm64 |
| `notarx-console` | The web console as static files and a reference nginx server block. | all |
| `notarxctl` | The command-line client. | amd64, arm64 |

Download the files of one release (here with the GitHub CLI) and install them from disk. Replace
`<version>` with the release version without the leading `v`, and `<arch>` with `amd64` or `arm64`
(`x86_64` or `aarch64` for RPM):

```sh
gh release download v<version> --repo mlops-ca/notarx --pattern 'notarx*_<version>_*.deb' --pattern 'notarx*-<version>.*.rpm' --pattern SHA256SUMS
```

Debian and Ubuntu:

<!-- install-snippet: deb -->
```sh
sudo apt install ./notarx_<version>_<arch>.deb            # the server
sudo apt install ./notarx-console_<version>_all.deb       # the console, optional
sudo apt install ./notarxctl_<version>_<arch>.deb         # the client
```

Fedora, RHEL, Rocky and Alma:

<!-- install-snippet: rpm -->
```sh
sudo dnf install ./notarx-<version>.<arch>.rpm            # the server
sudo dnf install ./notarx-console-<version>.noarch.rpm    # the console, optional
sudo dnf install ./notarxctl-<version>.<arch>.rpm         # the client
```

The packages do not start anything and do not touch your database. After installing the server
package, create the PostgreSQL database and user, put `NOTARX_DATABASE_URL` and the other settings
in `/etc/notarx/notarx.env`, then run the migration yourself and start the service:

```sh
sudo notarx-migrate --check   # exits non-zero until the schema is applied
sudo notarx-migrate
sudo systemctl enable --now notarx
```

`notarx-migrate` needs `psql` (`postgresql-client` is a recommended dependency), refuses a
migration whose checksum differs from the one recorded in the database, and is safe to run twice.
A package upgrade restarts a running service only when the schema is already current;
otherwise it prints a warning and leaves the service alone: run `notarx-migrate`, then
`sudo systemctl restart notarx`.

The console package puts its files in `/usr/share/notarx/console`; the example server block in
`/usr/share/doc/notarx-console/examples/nginx.conf` serves them and proxies the API and the
protocol paths to `127.0.0.1:8080`. The server itself does not serve the console.

## Homebrew

`notarxctl` for macOS and Linux (amd64 and arm64), from stable releases only:

```sh
brew install mlops-ca/tap/notarxctl
notarxctl version
```

The formula lives in [mlops-ca/homebrew-tap](https://github.com/mlops-ca/homebrew-tap) and is
updated by the stable release run from the release's checksums.

## Archives

The server (`notarx`) and the client (`notarxctl`) are attached to every release as
`<name>_<version>_<os>_<arch>.tar.gz` for `linux` and `darwin` on `amd64` and `arm64`, next to the
console build (`notarx-console_<version>_web.tar.gz`):

```sh
tar -xzf notarxctl_<version>_linux_amd64.tar.gz
./notarxctl version
```

`go install` of `cmd/notarxctl` is also supported ([CLI guide](cli.md)).

## Container images

The server and the console are published to the GitHub Container Registry as multi-architecture
images, signed by attestation like the archives:

| Image | Stable tags | Nightly tags |
| --- | --- | --- |
| `ghcr.io/mlops-ca/notarx` | `<version>`, `<major.minor>`, `latest` | `nightly`, `<version>-nightly.<N>` |
| `ghcr.io/mlops-ca/notarx-console` | the same | the same |

```sh
docker pull ghcr.io/mlops-ca/notarx:<version>
gh attestation verify oci://ghcr.io/mlops-ca/notarx:<version> --repo mlops-ca/notarx
```

Pin production deployments by digest, not by tag. The server image holds the gateway only: it has
no `psql` and no migrations. Apply the schema with the compose bundle or the `notarx` package
(`notarx-migrate`), or with `scripts/run-migrations.sh` from the release's source tree.

## Compose bundle

The bundle `notarx-compose-<version>.tar.gz` is attached to stable releases. It is the Compose
deployment of the [Oracle Cloud Always Free runbook](deployment/oci-free-tier.md) for one host,
with PostgreSQL, the migration job, notarx, the console and Caddy (automatic TLS). Every image in
it is pinned by the digest of the multi-architecture index built by that release run, so a moved
tag cannot change a deployed stack.

```sh
version=<version>
base=https://github.com/mlops-ca/notarx/releases/download/v$version
curl -fsSLO "$base/notarx-compose-$version.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify "notarx-compose-$version.tar.gz" --repo mlops-ca/notarx
sudo mkdir -p /opt/notarx
sudo tar -xzf "notarx-compose-$version.tar.gz" -C /opt/notarx --strip-components=1
```

On the OCI reference VM, cloud-init has created the data directories. On any other Docker host
create them first (the notarx container runs as uid 65532 and needs a private object root; set
`NOTARX_DATA_DIR` in `.env` to use another location):

```sh
sudo mkdir -p /srv/notarx/postgres /srv/notarx/objects
sudo chown 65532:65532 /srv/notarx/objects
sudo chmod 0700 /srv/notarx/objects
```

Then copy `.env.example` to `.env` (mode 0600), fill in the domain and the secrets, and start the
stack as described in the runbook. Leave the image variables of `.env` unset: setting one replaces
that image's digest pin. The migration runs as a Compose job before notarx starts, so
no manual `notarx-migrate` step is needed on this path. The `.deb` packages above are the
alternative for a host that already runs PostgreSQL and a reverse proxy.

## Check that the snippets stay true

`scripts/package-install-test.sh` extracts the two package snippets above (the blocks that follow
the `install-snippet` markers), substitutes the version and architecture, and runs them in
`debian:12` and `rockylinux:9` containers against the packages built from the same commit
(`make package-install-test`, run by CI and by the release workflow on both architectures). The
file names in this page are therefore the real ones.
