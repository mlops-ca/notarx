# Distribution Channels: Images, Archives, Native Packages and Signed Repositories

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md), [ADR 0029](0029-repository-visibility-public-private-feeds.md), [ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md).

## Context

notarx must be installable by people who do not build it. ADR 0013 renames the
artifacts, ADR 0016 builds them natively per architecture and attests them, and
ADR 0017 defines the Oracle Cloud Always Free reference deployment. None of them
decides which channels a user installs from, how packages are signed, or how a
release and a nightly reach those channels. Facts in the tree today:

- **Images.** `Dockerfile` builds a distroless static server image whose entrypoint
  is `/gateway` plus `/gateway-healthcheck`; it contains no `psql`, no migrations
  and no console. `Dockerfile.console` builds the React console and serves it from
  `nginxinc/nginx-unprivileged` using `deploy/kubernetes/console/default.conf`,
  which reverse-proxies `/api`, `/v2`, `/raw`, `/maven` and the other protocol
  prefixes to the server (`proxy_pass http://artifact-gateway:8080`, lines 12-29).
  **The server does not serve the console**; a package or compose stack needs a
  second component (nginx, Caddy or the console image) in front of it.
- **Migrations are SQL files plus a shell runner.** `scripts/run-migrations.sh`
  pipes `migrations/*.sql` into `psql` and records checksums in a ledger table
  (renamed to `notarx_schema_migrations` by ADR 0015 slice 1); `make migrate` runs
  it through the compose `migrate` service (`compose.yml:214-224`). There is no
  `gateway migrate` subcommand in `cmd/gateway/main.go` today; ADR 0022 slice 2 adds a Go
  runner (`internal/migrate`, `notarx migrate`) whose PostgreSQL ledger is byte-identical,
  while ADR 0022 keeps `run-migrations.sh` for PostgreSQL until its open question 2 decides
  to unify.
- **Release pipeline.** `.github/workflows/release.yml` has four jobs:
  `verify-candidate`, `build-assets`, `publish-images`, `publish-release`
  (GitHub Release via `softprops/action-gh-release`). `scripts/build-release-artifacts.sh`
  builds deterministic tar/zip archives; ADR 0016 slice 4 turns it into a native
  host-only four-leg matrix. ADR 0016 owns `release.yml` and selects GitHub
  attestations (`gh attestation verify oci://...`) as the provenance mechanism; ADR
  0013 states there is no CLI image (`notarxctl` ships as archives and `go install`).
- **Not present.** No nfpm or GoReleaser configuration, no `.deb` or `.rpm`, no
  Helm chart (`deploy/kubernetes` is kustomize), no cosign step, no Homebrew tap.
  `deploy/compose` holds only `apt-signer-rotation.yml`.
- **No RPM support in notarx.** A search for yum, rpm and dnf finds only fixtures and
  `docs/nexus-gap-analysis.md`. notarx's APT Hosted format is an unadvertised
  operator preview whose production-signing gates are open
  (`docs/apt-hosted-roadmap.md`).
- **Tooling constraints.** GoReleaser's split/merge builds are Pro-only
  (https://goreleaser.com/customization/partial/); nfpm
  (https://nfpm.goreleaser.com/) is standalone and packages one architecture per
  invocation, which fits ADR 0016's rule of no cross-compilation and no emulation.

ADR 0024 (release and nightly flow) defines the workflow job graph
`plan -> gates -> build -> publish` (plus `prune`), run for both channels: a nightly
builds the latest green commit with `X.Y.Z-nightly.N` stamped; a manually dispatched
stable run **rebuilds the exact commit of a verified nightly** on the same native matrix
and gates with the clean `X.Y.Z` stamped. Everything built in a stable run, including
packages, reports the stable version; nothing is relabelled from a nightly. This ADR
defines what `build` and `publish` produce and where they send it. ADR 0024 slices:
4 (build and publish jobs, nightly), 5 (stable build).

## Considered designs

1. **Release assets only.** `.deb`, `.rpm`, tarballs and images on GitHub Releases
   and GHCR; users download and `dpkg -i`. Zero infrastructure, but no upgrades via
   `apt upgrade` or `dnf upgrade`, no channel concept and no nightly.
2. **Managed package host (Cloudsmith OSS, packagecloud).** Cloudsmith's open-source
   plan covers deb and rpm hosting and signing with no servers to run, but carries
   storage and bandwidth quotas, an attribution requirement and a vendor
   dependency in the install path. packagecloud's free tier (2 GB storage) is too small.
3. **Self-hosted static repositories on Cloudflare R2 at `packages.mlops.ca`.**
   Repository metadata generated in CI, signed with a project key, synced to R2
   (free egress; storage about 0.015 USD per GB-month). Cost is key custody and a
   sync discipline; no vendor quota and no server to keep alive.
4. **Dogfood: host the repositories in notarx itself.** Attractive soak test, but
   there is no RPM support, the APT Hosted preview gates are open, and installing
   notarx from a notarx that is down is circular. Revisited by ADR 0026, which adds RPM
   Hosted and APT GA and lets notarx publish its own packages on a dedicated instance
   while R2 stays the bootstrap source.
5. **Source-build services (Launchpad PPA, COPR, OBS).** Rejected: builds are
   offline and need vendored Go 1.26 sources, and the shipped binary would not be the
   binary the native test lanes exercised.

## Decision

Option 3 for apt and yum, with option 1 as the canonical artifact store and option 2
as a documented fallback mirror. Option 4 is realised as a second, equal-trust source: ADR 0026
Part D publishes the same packages to public notarx feeds at `feeds.mlops.ca` (nightly first,
stable after the ADR 0026 promotion criteria; Milestone Public feeds, requiring ADR 0029 public
visibility), and `packages.mlops.ca` on R2 remains the bootstrap and fallback source.

### Channel matrix

| Channel | Artifact | Where | Stable | Nightly |
|---|---|---|---|---|
| Images | `ghcr.io/mlops-ca/notarx`, `notarx-console`, `notarx-scanner` (optional, ADR 0030; multi-arch index; ADR 0016), all signed and attested and all covered by the release gate | GHCR | tags `<version>`, `<major.minor>`, `latest` | tags `nightly`, `<version>-nightly.<N>` (retained per ADR 0024) |
| Archives | `notarx`, `notarxctl`, `notarx-signer` (ADR 0026; no `pkcs11` build tag in the default archive) tar.gz (zip not needed: Windows is out per ADR 0016) | GitHub Release | yes | GitHub pre-release |
| Integrity | one `SHA256SUMS` over every asset, plus a GitHub attestation per file (`actions/attest`; `actions/attest-build-provenance` is a wrapper over it) | GitHub Release | yes | yes |
| Compose bundle | `notarx-compose-<version>.tar.gz`, image references pinned by the digests of that run's own build | GitHub Release | yes | no |
| Native packages | `notarx`, `notarx-console`, `notarxctl` as `.deb` and `.rpm` | GitHub Release and apt/yum repos | `stable` suite | `nightly` suite |
| apt / yum repos | signed static repositories | `https://packages.mlops.ca` (R2) | `stable` | `nightly` |
| Homebrew | `mlops-ca/homebrew-tap`, `Formula/notarxctl.rb` | tap repo | yes | no |
| notarx feeds | the same deb and rpm packages in notarx-hosted public repositories, ADR 0026 Part D | `https://feeds.mlops.ca` | after the ADR 0026 promotion criteria | `nightly` suite and `notarx-nightly` |
| Helm chart (OCI) | `oci://ghcr.io/mlops-ca/charts/notarx` | GHCR | Backlog | Backlog |
| cosign signatures | keyless signature on the image index digest | GHCR | Backlog | Backlog |

Images and archives follow ADR 0016 unchanged; this ADR adds nothing to their build.
`gh attestation verify` is the verification story for every GitHub-hosted asset;
cosign is added only if a consumer needs a non-GitHub verifier (for example a
Kyverno policy).

### Packaging without GoReleaser Pro

A single `packaging/nfpm.yaml` family (one file per package, driven by `VERSION` and
`ARCH` environment variables, `version_schema: none` so nfpm does not reformat the version, `release` per format) is run by the `build` matrix of ADR 0016 slice 4 on each
native runner after the archive is built: `nfpm package -p deb` and `-p rpm`. There is
no GoReleaser dependency and no second source of truth; `build-release-artifacts.sh`
stays the only compiler of binaries, and packages repack its output so within one run the
packaged binary is byte-identical to the archived and tested one. Only Linux legs produce
packages: `ubuntu-24.04` gives `amd64` / `x86_64`, `ubuntu-24.04-arm` gives `arm64` /
`aarch64`.

Packages:

- **`notarx` (server, per architecture).** `/usr/bin/notarx`,
  `/lib/systemd/system/notarx.service`, `/etc/notarx/notarx.env`
  (`config|noreplace`, mode `0640 root:notarx`), `/usr/share/notarx/migrations/` (the repository's
  `migrations/` layout; ADR 0022 slice 3a moves PostgreSQL files to `migrations/postgres/`, and the
  package mirrors whatever the runner reads),
  `/usr/share/notarx/run-migrations.sh`, `/usr/bin/notarx-migrate` (wrapper), a
  `sysusers.d` entry. Depends on nothing at run time; `Recommends: postgresql-client`
  (the migration runner needs `psql`).
- **`notarx-console` (architecture-independent).** Static assets in
  `/usr/share/notarx/console` and a reference nginx server block in
  `/usr/share/doc/notarx-console/examples/nginx.conf` that proxies to
  `127.0.0.1:8080`. `Recommends: nginx`. Built once (on the amd64 leg) because the
  assets are identical per architecture; nfpm `arch: all`. Embedding the console in
  the server is a larger change and is an open question, not decided here.
- **`notarxctl` (per architecture).** `/usr/bin/notarxctl` only. Separate package so
  workstations and CI runners do not pull a service.

Systemd unit: `User=notarx`, `Group=notarx`, `EnvironmentFile=/etc/notarx/notarx.env`,
`StateDirectory=notarx` (`/var/lib/notarx`, used as the default
`NOTARX_OBJECT_STORE_FS_ROOT` for the ADR 0017 filesystem driver), `Restart=on-failure`
and hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
`ReadWritePaths=/var/lib/notarx`, `CapabilityBoundingSet=` empty, `RestrictAddressFamilies`
limited to inet, inet6 and unix). The user is created in `preinst` via `sysusers`
semantics, not a login shell.

**No migrations in `postinst`, no automatic start.** Maintainer scripts run as root
with no way to report a failed schema change atomically and may run during an
unattended upgrade. `postinst` only runs `systemctl daemon-reload` and prints
`run: notarx-migrate, then: systemctl enable --now notarx`. `notarx-migrate` reads
`NOTARX_DATABASE_URL` from `notarx.env`, passes it to the unchanged runner, and
refuses to run if the ledger records a checksum that differs (ADR 0015 behavior).
On upgrade, `postinst` does not restart the service when the packaged baseline or
migration set differs from the applied ledger; it prints a warning instead
(`systemctl try-restart` happens only when `notarx-migrate --check` exits 0). A
Go-native runner (ADR 0022 slice 2, `notarx migrate`) does not exist yet. Until it lands
the wrapper drives `run-migrations.sh` (hence `postgresql-client`). Once it has landed,
the wrapper keeps its name and flags and delegates to `notarx migrate`, and the
`psql` recommendation is dropped when ADR 0022's open question 2 unifies the PostgreSQL
path on the Go runner (Backlog slice 12).

### Compose bundle and the OCI free-tier deployment

`deploy/oci-free-tier` (ADR 0017 slice 6) is rendered at release time into
`notarx-compose-<version>.tar.gz`: `compose.yml`, `Caddyfile`, `.env.example`,
`migrations/`, `run-migrations.sh`, cloud-init template and a README. Image
references are `ghcr.io/mlops-ca/notarx@sha256:<digest>` resolved from the
multi-arch index built by the stable run (the optional `notarx-scanner` image of ADR 0030 is pinned the same way), so the bundle is reproducible and a tag move
cannot change a deployed stack. The Always Free A1 runbook installs from this bundle
(compose supplies PostgreSQL, the `migrate` job, Caddy and the console image, which
is why the console question does not arise there). The `.deb` path serves hosts that
already run PostgreSQL and a reverse proxy; the runbook mentions it as an alternative.

### Repository layout (`packages.mlops.ca`)

```
/gpg.key                                         ASCII-armored public key (also /notarx.asc, same bytes)
/apt/dists/{stable,nightly}/{InRelease,Release,Release.gpg}
/apt/dists/<suite>/main/binary-{amd64,arm64}/{Packages,Packages.gz}
/apt/dists/<suite>/main/binary-all/...           (notarx-console)
/apt/pool/main/n/notarx/*.deb
/rpm/{stable,nightly}/{x86_64,aarch64,noarch}/*.rpm
/rpm/{stable,nightly}/repodata/{repomd.xml,repomd.xml.asc,*-primary.xml.zst,...}
```

Tooling: `aptly` (publishes with `-acquire-by-hash`, which removes the
hash-mismatch race that reprepro leaves) and `createrepo_c`. Both run on a clean
runner and rebuild each suite **statelessly** from the retained package set on every
run; no aptly database or repodata is persisted between runs.

**Versions and channels.** Debian and RPM both need nightlies to sort below the
release they precede:

| | Debian | RPM |
|---|---|---|
| stable | `0.2.0-1` | `0.2.0-1` (`Version: 0.2.0`, `Release: 1`) |
| nightly | `0.2.0~nightly.3-1` | `0.2.0-0.nightly.3` |

Debian orders `0.1.0 < 0.2.0~nightly.3 < 0.2.0~nightly.10 < 0.2.0` because `~` sorts
before the empty string; `3 < 10` needs a numeric field, so `N` is a plain
non-zero-padded counter from ADR 0024 (strictly increasing, never reused). RPM
achieves the same with `Release: 0.nightly.N < 1`. Before the first tag nightlies
are `0.1.0~nightly.N`. Both orderings were checked: `0.2.0~nightly.3-1 < 0.2.0-1` in dpkg
(a `~` sorts before everything, even the end of the string) and `0.2.0-0.nightly.3 < 0.2.0-1`
in rpm (the first Release segment is `0` against `1`). nfpm's default `version_schema: semver`
reformats versions in ways its documentation does not specify, so the plan passes explicit
strings and the `package-check` target asserts the resulting `Version`/`Release` fields with
`dpkg-deb -f` and `rpm -qp`. RPM signing sets `key_id` to the signing subkey explicitly
(rpm older than 4.13 cannot verify subkey signatures; EL8 and newer can). A stable package
is produced by the stable run's own `build` and carries the stable version; it is never a
renamed nightly package. A user on `stable` never sees a nightly; a user on `nightly`
moves to `0.2.0` when it ships because the stable version outranks every nightly of
that series, then receives `0.3.0~nightly.N` as development continues. Both suites
may be enabled at once; apt pinning is out of scope and the docs say to enable one.

**Retention.** `nightly` keeps the last 14 builds per package and architecture
(older ones are dropped from the next regeneration); ADR 0024's pruning keeps at least the
newest 14 nightly releases, so the inputs for regeneration always exist. `stable` keeps every release
(a few MB each) so any version can be pinned for rollback.

### Trust model and signing

- One OpenPGP key `notarx packages <packages@mlops.ca>`, **distinct from the
  reference APT signer fixture key and from customer signers**. RSA 4096 primary
  (certify only, kept offline on hardware token plus an offline backup) with one
  RSA 4096 signing subkey valid two years (S1, in the `packages-signing` environment). The
  same certificate carries a second RSA 4096 signing subkey S2 held in a KMS behind
  `notarx-signer` for the notarx-hosted feeds (ADR 0026 Part D); the primary stays offline,
  the two subkeys are never in the same place, and `gpg.key` publishes both, so clients switch
  between R2 and the feeds without a key change. RSA, not Ed25519, because EL8-era `rpm`
  and `gnupg` cannot verify EdDSA. A revocation certificate is generated at creation
  and stored offline separately.
- The armored subkey (never the primary) is stored as an Actions secret scoped to the
  protected environment **`packages-signing`**: required reviewers, deployment
  branches and tags restricted to the release tag pattern, no self-review.
- **Separation of duties.** `sign-repo` runs in `packages-signing`, holds the subkey,
  has no R2 credentials and writes the regenerated, signed tree as a workflow
  artifact. `sync-repo` runs in a second environment `packages-publish`, holds a
  Cloudflare API token limited to the one bucket, has no signing key, and
  before uploading verifies every `InRelease`, `Release.gpg` and `repomd.xml.asc`
  against `gpg.key`. A compromise of either job alone cannot ship an unsigned or
  forged repository.
- **Input provenance.** The retained package set is assembled from GitHub Release
  assets (and ADR 0024's nightly releases), each verified with
  `gh attestation verify`, **not** from the R2 pool. R2 is a derived cache; a
  tampered bucket cannot cause the key to sign foreign packages.
- Packages are not individually signed in v1; the signed repository metadata carries
  package hashes (apt) or `repo_gpgcheck` plus `gpgcheck` over `repomd.xml` and
  package signatures (rpm). nfpm RPM signing is enabled with the same subkey in
  `sign-repo`, so `gpgcheck=1` is meaningful; `.deb` files in the pool are covered
  by `Release` hashes.
- Rotation: the subkey expires in two years with a reminder at six months; the
  new key fingerprint is announced in release notes and docs. A
  `notarx-archive-keyring` package that ships the key so apt/dnf can rotate it is
  Backlog; until then, rotation needs users to refetch `gpg.key` (documented).

### Install snippets (reference for `docs/install.md`)

Debian/Ubuntu (deb822; no `apt-key`):

```bash
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://packages.mlops.ca/gpg.key -o /etc/apt/keyrings/notarx.asc
sudo tee /etc/apt/sources.list.d/notarx.sources >/dev/null <<'EOF'
Types: deb
URIs: https://packages.mlops.ca/apt
Suites: stable
Components: main
Architectures: amd64 arm64
Signed-By: /etc/apt/keyrings/notarx.asc
EOF
sudo apt update && sudo apt install notarx notarx-console   # or: notarxctl
```

Use `Suites: nightly` for the nightly channel.

Fedora/RHEL/Rocky/Alma:

```ini
# /etc/yum.repos.d/notarx.repo
[notarx]
name=notarx stable
baseurl=https://packages.mlops.ca/rpm/stable/$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://packages.mlops.ca/gpg.key
```

```bash
sudo dnf install notarx notarx-console   # then: notarx-migrate; systemctl enable --now notarx
```

Homebrew: `brew install mlops-ca/tap/notarxctl`. The formula's `url`/`sha256` come
from the darwin/linux amd64/arm64 archives; the tap is updated by the stable `publish` job through
a GitHub App installation token scoped to that single repository and has a `test do`
block that runs the binary's version command.

### Integration with ADR 0024 jobs

| Job | Responsibility from this ADR |
|---|---|
| `plan` | computes the package version strings (`0.2.0-1`, `0.2.0~nightly.N-1`, `0.2.0-0.nightly.N`) and the channel (`nightly` or `stable`) from the plan's `version`; `release-version-check` stays the single version gate |
| `build` | native matrix legs run nfpm after the archive with the stamped version; uploads packages and the bundle inputs; attests every file |
| `publish` (nightly) | attaches packages, tarballs, `SHA256SUMS` to the GitHub pre-release; runs `sign-repo` and `sync-repo` for the `nightly` suites |
| `publish-feeds`, `verify-feeds` (both channels, after `publish`) | owned by ADR 0026 slices 15, 16 and 22 (`verify-feeds` is slice 22): publish the same packages to `feeds.mlops.ca` and compare bytes with R2 |
| `publish` (stable) | attaches the stable run's packages, tarballs, `SHA256SUMS` to the GitHub Release; runs `sign-repo` and `sync-repo` for `stable`; renders the compose bundle pinned to that run's image digests; updates the Homebrew tap |

Stable packages, archives, images, the Homebrew formula and `notarxctl version` all
come from the stable run, which rebuilds the verified nightly's commit with the clean
version (ADR 0024). Nothing is relabelled from a nightly: re-stamping a finished
package would change its checksum and break its attestation. Because the stable
and nightly packages are different builds of one commit, the repository retains both
sets independently.

**Sync order (index last).** `sync-repo` serializes through a workflow concurrency
group `packages-repo` (R2 has no locks) and uploads in four passes: (1) `pool/` and
`rpm/**/*.rpm` plus new `by-hash` and hashed `repodata` files, additive only; (2)
`dists/**` indices and `repomd.xml`; (3) `repomd.xml.asc` and `InRelease`/`Release.gpg`
last; (4) deletions of retired files, after indices stop referencing them. Pool and
hashed files are `Cache-Control: public, max-age=31536000, immutable`; indices and
`repomd.xml*` are `max-age=60`. Because rpm signs `repomd.xml` with a detached
signature, there is a seconds-long window where `repo_gpgcheck` can fail; clients
retry, and the post-publish check below tolerates one retry.

**Post-publish verification** (`verify-repo` job, in `publish` for both channels): fresh
`debian:12`, `ubuntu:24.04`, `rockylinux:9` and `fedora` containers on both native
runners add the repository exactly as the snippets above, install `notarxctl` and
`notarx`, and assert `apt-cache policy`/`dnf list` show the expected version and the
`~`/`0.nightly` ordering. A failure blocks the stable `publish` job from reporting success.

### Hosting facts (Cloudflare R2)

The custom domain `packages.mlops.ca` must be a zone in the same Cloudflare account as
the bucket (a partial CNAME setup works for a domain whose DNS is elsewhere); `r2.dev` is
rate-limited and unsupported for production, so it is only used in the staging bucket.
Cloudflare caches only some file types by default, so a Cache Rule covers `.deb`, `.rpm`,
`.xml`, `.zst`, `Packages*`, `Release`, `InRelease` and `.asc`, and the `Cache-Control`
headers above are set on upload through the S3 API. Egress is free; storage is
0.015 USD per GB-month with a 10 GB free tier, and the four-pass sync is the main source
of Class A operations (4.50 USD per million).

### Fallback

The notarx feeds (`feeds.mlops.ca`, ADR 0026) are a second equal-trust source signed by the
same certificate (subkey S2); R2 remains the bootstrap source and the fallback, and the install
docs list R2 first. If R2 or the custom domain is unavailable for an extended period, the same generated
trees are mirrored to Cloudsmith OSS (documented, manual until Backlog slice 11) and
the docs list a second URL. Release assets always allow `dpkg -i` / `rpm -i`.

## Security considerations

- Subkey exfiltration is the main risk: mitigated by environment protections, no
  R2 credentials in the signing job, short-lived subkey, offline primary and a
  rehearsed revocation (slice 4 includes a revocation drill on the staging key).
- No cosign in v1; GitHub attestations bind artifacts to the workflow identity. The
  repository key proves publication by the project, not build provenance; docs say so.
- Packages never run migrations or start services, so an `apt upgrade` cannot
  corrupt the schema or take a node down unattended.
- `notarx.env` holds secrets (`NOTARX_DATABASE_URL`, tokens) and is `0640 root:notarx`
  and `noreplace`; the compose `.env.example` holds placeholders only.
- Third-party actions in new jobs are pinned by SHA like existing ones.

## Consequences

- Users get native `apt upgrade` / `dnf upgrade` and a nightly track without the
  project running servers; cost is a small R2 bill and key custody.
- Within one run, bit-identical packaged and archived binaries tie into the ADR 0016 native-test claim.
- Stable packages, archives and images come from the stable run's own build of the
  verified nightly commit, so they carry the clean version everywhere; they are not
  byte-identical to the nightly's (ADR 0024 rejects digest promotion for that reason).
- A second source of release outputs (`packaging/`) must stay in step with the
  archive builder; `make package-check` guards the contents list.
- notarx joins its own distribution path through ADR 0026 (RPM Hosted, APT GA, the
  `feeds.mlops.ca` instance); until that ships, R2 is the only package source.

## Implementation slices

Every slice follows repository rules: no cross-compilation, English docs, `make
docs-check`, and `make architecture-check` when Go packages change. Slices touching
`release.yml` go through ADR 0016 slice 3 for names and landing order.

1. **Package definitions and service layout.**
   Milestone: Foundation.
   Scope: `packaging/` (`nfpm-notarx.yaml`, `nfpm-notarx-console.yaml`,
   `nfpm-notarxctl.yaml`, `notarx.service`, `notarx.env`, `sysusers`, `preinst`,
   `postinst`, `prerm`), `scripts/notarx-migrate`, `Makefile` target `package-check`.
   - [ ] `package-check` builds all three packages from a local build and asserts
     contents, modes, `Recommends`, absence of migration or start logic in scripts.
   - [ ] Container install test (debian:12 and rockylinux:9, native arch): package
     installs, `notarx` user exists, service is installed but not enabled,
     `notarx-migrate` applies the baseline against a PostgreSQL 16 container.
   - [ ] `systemd-analyze verify` passes on the unit.
   - [ ] `make test`, `make lint`, `make docs-check`, `make architecture-check` pass.
   Dependencies: ADR 0013 slice 3 (binary names) and slice 4 (env prefix); ADR 0015
   slice 4 (baseline filename and ledger table).

2. **Native package build legs and release assets.**
   Milestone: Foundation.
   Scope: `.github/workflows/release.yml`, `ci.yml` (`mainline-assets`), the matrix
   from ADR 0016 slice 4, `scripts/write-release-checksums.sh`.
   - [ ] amd64 and arm64 Linux legs produce deb and rpm on their own runner; the
     console package is built once; packaged binary hash equals the archive binary
     hash (asserted in a script test).
   - [ ] One `SHA256SUMS` covers archives and packages; each file is attested.
   - [ ] Package install smoke test runs on the producing architecture.
   - [ ] `make release-artifacts-test`, `make release-readiness-check`, `make test`,
     `make lint` pass.
   Dependencies: slice 1; ADR 0016 slice 4; ADR 0024 slice 4 (build and publish jobs).

3. **Compose bundle release asset.**
   Milestone: Foundation.
   Scope: `scripts/render-compose-bundle.sh`, `release.yml` (`publish`, stable channel), tests.
   - [ ] Bundle contains compose, Caddyfile, `.env.example`, migrations and runner,
     with every image pinned by digest for both architectures.
   - [ ] `docker compose config` passes on the extracted bundle; no secrets in it.
   - [ ] `make release-readiness-check` and `make docs-check` pass.
   Dependencies: ADR 0017 slice 6; ADR 0016 slice 3; ADR 0024 slice 5 (stable build).

4. **Key ceremony, environments and R2 bucket (operations runbook).**
   Milestone: Foundation.
   Scope: `docs/release/packages-signing.md`, repository settings checklist script
   `scripts/check-packages-environments.sh` (reads via `gh api`), staging bucket.
   - [ ] Key creation, subkey, revocation certificate, custody and rotation steps
     documented; fingerprint recorded in `SECURITY.md`.
   - [ ] Environments `packages-signing` and `packages-publish` exist with required
     reviewers and tag restrictions (checked by the script).
   - [ ] The ceremony documents certifying a second signing subkey (S2, held in a KMS for
     ADR 0026) with the primary staying offline, and `gpg.key` publication of all subkeys.
   - [ ] A revocation and subkey-rotation drill ran on the staging key and the
     result is linked in the PR.
   - [ ] `make docs-check` passes.
   Dependencies: none (may start immediately).

5. **Stateless repository generator.**
   Milestone: Foundation.
   Scope: `scripts/build-package-repo.sh`, `scripts/build-package-repo-test.sh`,
   `Makefile` target `package-repo-test`.
   - [ ] Given a directory of verified debs and rpms and a throwaway key, produces
     apt `stable` and `nightly` suites (by-hash on) and rpm trees with signed
     `repomd.xml`.
   - [ ] Retention: 15 synthetic nightlies leave 14; stable keeps all.
   - [ ] Version-order test with `dpkg --compare-versions` and `rpmdev-vercmp` for
     `0.1.0 < 0.2.0~nightly.3 < 0.2.0~nightly.10 < 0.2.0` and the RPM equivalent.
   - [ ] Container clients (debian:12, ubuntu:24.04, rockylinux:9) install from a
     local file server with `Signed-By` and `gpgcheck=1`/`repo_gpgcheck=1`; a
     tampered index is rejected.
   - [ ] `make package-repo-test`, `make lint` pass.
   Dependencies: slice 1.

6. **Publish wiring for repositories.**
   Milestone: Foundation.
   Scope: `release.yml` (`publish` jobs, both channels; owned by ADR 0024), `scripts/sync-package-repo.sh`.
   - [ ] `sign-repo` (environment `packages-signing`) and `sync-repo`
     (`packages-publish`) are separate jobs; the sync job verifies signatures
     before upload; concurrency group `packages-repo`.
   - [ ] Inputs are verified with `gh attestation verify`; R2 is never an input.
   - [ ] Four-pass upload order is asserted by a dry-run test with a fake S3
     endpoint that records request order (indices after all pool files).
   - [ ] `verify-repo` containers pass on both native runners against the staging
     bucket, then production.
   - [ ] `make release-readiness-check`, `make lint` pass.
   Dependencies: slices 2, 4, 5; ADR 0024 slices 4 and 5 (publish jobs, stable build);
   ADR 0016 slice 3.

7. **Homebrew tap.**
   Milestone: Foundation.
   Scope: new repository `mlops-ca/homebrew-tap`, `scripts/render-homebrew-formula.sh`,
   `release.yml` (`publish`, stable channel).
   - [ ] Formula rendered from archive checksums for darwin and linux, both
     architectures; `brew audit --strict` and `brew test` pass in tap CI.
   - [ ] GitHub App token limited to the tap repository; the step runs only on stable.
   Dependencies: ADR 0016 slice 4; ADR 0024 slice 5.

8. **Install documentation and 0017 alignment.**
   Milestone: Foundation.
   Scope: `docs/install.md`, `docs/deployment/oci-free-tier.md`, `docs/README.md`
   and `docs/site-map.json` entries.
   - [ ] Snippets match slice 5 test fixtures (a script extracts them and runs them
     in containers).
   - [ ] Documents channel switching, key fingerprint, verification with
     `gh attestation verify`, and the manual migration step.
   - [ ] `make docs-check` passes.
   Dependencies: slices 3, 6, 7.

9. **Helm chart as OCI artifact.**
   Milestone: Backlog.
   Scope: `deploy/helm/notarx`, `release.yml` (`publish`, stable: `helm push oci://ghcr.io/mlops-ca/charts`).
   - [ ] `helm lint`, `helm template` against kustomize parity, chart attested.
   Dependencies: slice 6.

10. **cosign keyless signatures on image index digests.**
    Milestone: Backlog.
    - [ ] `cosign verify` documented with the workflow identity; admission-policy example.
    Dependencies: ADR 0016 slice 3.

11. **Cloudsmith OSS mirror.**
    Milestone: Backlog.
    - [ ] stable `publish` pushes the same package set; attribution page added; docs list the fallback URL.
    Dependencies: slice 6.

12. **Packages adopt the Go migration runner.**
    Milestone: Backlog.
    - [ ] `notarx-migrate` delegates to `notarx migrate` (ADR 0022 slice 2, same ledger
      semantics, tested with the shared vectors of `migration-runner-check.sh`) with the same
      name and flags; the `psql` recommendation is removed once ADR 0022's open question 2 unifies PostgreSQL on the Go runner.
    Dependencies: slice 1; ADR 0022 slice 2.
13. **notarx-hosted nightly and stable feeds (tracking slice).**
    Milestone: Public feeds.
    - [ ] The RPM hosted format is delivered by ADR 0026 slices 7 to 14 and 19 to 21 (not here).
    - [ ] The nightly suite served by the notarx instance `feeds.mlops.ca` mirrors R2 content
      (ADR 0026 slice 15) and stable follows after the ADR 0026 promotion criteria (slice 16; `verify-feeds` is slice 22);
      R2 stays the bootstrap and fallback source, with the same certificate and a separate signing subkey.
    Dependencies: slice 6; ADR 0026 slices 6, 15, 16 and 22 (APT GA, feeds instance, stable suite, `verify-feeds`).

14. **`notarx-archive-keyring` package.**
    Milestone: Backlog.
    - [ ] Package ships `gpg.key` so apt/dnf key rotation is an upgrade; rotation drill documented.
    Dependencies: slice 6.

## Open questions

1. Is serving the console from the server (embedding, with `Cache-Control` and SPA
   fallback) preferable to a separate `notarx-console` package and nginx? Deferred
   until after Foundation; the package split above does not block it.
2. Is `packages@mlops.ca` operated as a monitored mailbox (needed for key
   uid)? If not, the uid uses a project issues URL.
