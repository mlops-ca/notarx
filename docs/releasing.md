# Releasing notarx

[Documentation index](README.md)

The release design is [ADR 0024](adr/0024-release-versioning-and-conventional-commits.md). This page is the
operator runbook: how versions are chosen, what runs when, how to verify a release and how to keep the
pipeline alive. The stable channel (a manual rebuild of a verified nightly commit) arrives with a later slice of the
ADR; today the pipeline publishes nightly pre-releases.

## Versions (the 0.x rules)

- Versions are SemVer 2.0. The committed tree always carries `0.0.0-dev`; release builds stamp the real
  version (`scripts/stamp-version.sh`). No release commit changes version files.
- While the major is 0, `feat` bumps the minor, `fix`, `perf` and `revert` bump the patch, and a breaking change
  (`!` or a `BREAKING CHANGE:` footer) bumps the minor. Other types never release. `1.0.0` is produced only by an
  explicit `version_override` of the stable run.
- A nightly is `X.Y.Z-nightly.N` with tag `vX.Y.Z-nightly.N`. `X.Y.Z` is the next stable version computed from the commits since
  the last stable tag (`0.1.0` before any stable tag). `N` is the highest existing counter for that core plus one, so pruning
  never reuses a number.
- Commit titles follow `.github/commit-policy.json`; the pull request title becomes the squash commit subject.

## Milestones

Milestones are themed, never numbered, because versions are computed: Foundation, Data scale, Identity & federation,
Promotion chains, MySQL HeatWave, Formats, Public feeds, Supply-chain security and Backlog. Unscheduled work has none.
A milestone closes when its theme is complete. `scripts/repo-governance-apply.sh` creates exactly these nine.

## The nightly pipeline

`.github/workflows/release.yml` runs at 03:17 UTC and on `workflow_dispatch` (`dry_run` defaults to true). A dry run
plans, runs the gates and builds the archives and images, so the gate and build paths are exercised on demand; it pushes no image
(the images are built and checked natively and loaded locally), and it publishes nothing: no `publish` job, no release, tag,
package or attestation. Only `publish` and `prune --apply` write. Commit parsing for the plan, the commit-policy warnings and the
release notes starts at the previous release or, before the first one, at the fork commit recorded in `docs/upstream.md`
(`scripts/release-plan.sh fork`), so upstream history never produces warnings. Its jobs:

1. `plan` (read-only) runs `scripts/release-plan.sh nightly`: the tip of `main`. It reads no check runs, because pushes to
   `main` run no tests (ADR 0024, amendment of 2026-11-19). It skips, with a notice, when that commit was already
   released or when no releasing commit exists since the previous release.
2. `gates` runs the complete test suite (`.github/workflows/test-suite.yml`, the same definition pull request CI uses) on the
   planned commit: `quality` and `quality-arm64`, `console`, the Console E2E shards, `object-store-contract`, `docs`,
   `image-test-stages` and every contract lane on both architectures. A gate failure means no release that night; the
   liveness alert covers a run of failed nights. Nothing is built or published before the gates pass.
3. `build` calls the ADR 0016 reusable workflows: `image-build.yml` (per-architecture images, merged index, attestations)
   for every image in `scripts/release-images.json`, and `release-archives.yml` (four native runners). Each Linux runner also
   repacks the archives it built into `.deb` and `.rpm` packages with the pinned nfpm (`scripts/build-packages.sh`;
   `notarx` and `notarxctl` per architecture, `notarx-console` once on amd64), checks them (`scripts/package-check.sh`:
   metadata, modes, maintainer scripts and binary hashes equal to the archive) and installs them in a container on that
   architecture. Packages are not signed (ADR 0025 amendment); `apt install ./<file>.deb` or `dnf install ./<file>.rpm`.
4. `publish` writes `SHA256SUMS` and `release-manifest.json` (`scripts/release-manifest.sh`), attests the archives and packages, verifies every
   attestation, and as its last step runs one `gh release create vX.Y.Z-nightly.N --prerelease --target <sha>`, which creates the
   tag and the pre-release together. A failed leg therefore leaves no tag and the next night retries.
5. `prune` applies retention after a successful nightly, and as a dry run on `workflow_dispatch`.

Jobs owned by other ADRs (channels, the public feeds) declare `needs: publish` and never change `plan`.

A stable release (`scripts/release-plan.sh stable`) promotes the commit of a nightly tag and reads no check runs either. That
commit was published only after the gates of its nightly run passed on it, so the stable run reuses those results; it never
releases a commit whose release-run gates did not pass.

Adding a published image is one entry in `scripts/release-images.json` ; the manifest script fails unless
every listed image has a recorded index digest.

## Verifying a release

Download the release assets, then:

```
gh attestation verify notarx_<version>_linux_amd64.tar.gz --repo mlops-ca/notarx   --signer-workflow mlops-ca/notarx/.github/workflows/release.yml
gh attestation verify oci://ghcr.io/mlops-ca/notarx@<index digest from release-manifest.json> --repo mlops-ca/notarx   --signer-workflow mlops-ca/notarx/.github/workflows/image-build.yml
(cd <dir> && sha256sum -c SHA256SUMS)
```

Image attestations are made inside the reusable `image-build.yml`, so the signer is that called workflow, and
`--signer-workflow` must be repo-qualified (`owner/repo/path`); a bare `.github/workflows/image-build.yml` fails.
`--signer-repo` alone is not used because it accepts any workflow of the repository. Add `--source-ref refs/tags/<tag>` or
`--cert-identity` only to pin a tag. `scripts/verify-release-attestations.sh` runs this for every image and archive of a
manifest; its fixture test (`make release-pipeline-test`) checks that the repo-qualified identity is accepted and that a bare path,
another workflow and another repository are rejected. The same flags verify any real nightly by hand.

## Retention

`scripts/prune-releases.sh` keeps the newest 14 nightlies, the newest nightly of every core and every nightly whose commit equals a
stable tag's commit; it deletes the rest with `gh release delete --cleanup-tag` and never touches stable releases or tags.
`scripts/prune-ghcr.mjs` is mark and sweep: it marks every tagged version that is not an expired nightly, plus its child manifests
and its attestation (referrer) manifests, and deletes only unmarked versions older than seven days (the in-flight build guard; there is no other age cutoff, so a long-lived
retained nightly never holds the sweep back, and reachability from retained tags protects retained images). It aborts when the mark set is empty and never uses a bulk "delete untagged" action, because the per-architecture
digests behind an index are untagged. Both scripts default to a dry run (the workflow applies them only after its own nightly was
published). Mainline tags (`main`, `main-sha-*`) are treated as live and are not pruned by this slice. To rehearse:

```
gh workflow run release.yml -f dry_run=true
```

## Repository governance

`scripts/repo-governance-apply.sh --repo mlops-ca/notarx --app-id <id> [--reviewer <login>]` (repository admin, idempotent) applies:

| Item | Setting |
| --- | --- |
| Merge methods | squash only, default message is the PR title and body |
| Ruleset `stable-tags` | `refs/tags/v*` excluding `v*-nightly.*`: block update and deletion, no bypass |
| Ruleset `nightly-tags` | `refs/tags/v*-nightly.*`: block update; deletion only by the release App |
| Ruleset `tag-creation` | `refs/tags/v*`: creation restricted to the release App |
| Environment `stable-release` | optional required reviewers (`--reviewer`) on the stable publish job |
| Milestones | the nine themed milestones |

It prints the effective settings and rulesets at the end. It refuses to run without `--app-id` because tag creation is restricted to
the App.

### Release App registration (manual, once)

1. Create a GitHub App owned by the organization with repository permissions Contents, Pull requests and Packages: read and write;
   no webhook, no user permissions.
2. Install it on this repository only; note the numeric App id.
3. Set the repository variable `RELEASE_APP_ID` and the secret `RELEASE_APP_PRIVATE_KEY` (the PEM key).
4. Run the governance script with `--app-id`. Until step 3 the workflow falls back to the workflow token for tag creation and
   pruning; after step 4 only the App can create or delete release tags.

## Scheduled-workflow liveness

GitHub disables scheduled workflows after 60 days without repository activity. Two measures:

- `release.yml` calls the workflow-enable API (`PUT /repos/{repo}/actions/workflows/release.yml/enable`) on every scheduled run, in a
  separate job so that `plan` stays read-only. This is best effort only. Empirical outcome (2026-11-19): the call on an enabled workflow
  returns `204 No Content` and changes nothing visible; whether it revives a schedule that GitHub disabled for inactivity cannot be shown
  without waiting out the timer, and GitHub documents only repository activity as resetting it. Nothing relies on the call.
- `.github/workflows/release-liveness.yml` runs monthly and opens an issue titled "release: no nightly run in 35 days" when the nightly
  workflow recorded no successful scheduled run (a nightly or a skip notice) in 35 days. The monthly alert, not the call, is the backstop.
  It is itself a scheduled workflow, so a repository that is quiet for 60 days disables it together with the nightly; it therefore
  also runs on every push to `main`, which raises the alert as soon as activity resumes. When it fires: re-enable the workflow in
  the Actions tab, then dispatch it once.
