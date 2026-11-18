# Changelog

[Documentation index](docs/README.md)

This file records stable notarx releases. It is generated from conventional
commit subjects (the squash-merged pull request titles) and `BREAKING CHANGE:`
footers when a stable release is published; do not edit it by hand. See
[ADR 0024](docs/adr/0024-release-versioning-and-conventional-commits.md).

notarx is a hard fork of `ccsert/artifact-gateway`, forked from upstream `0.9.0`
(commit `c4d5081cc53509f417603dfc37368725eacffb62`, the last upstream commit
before the first notarx commit; it includes later upstream work). The record of
picked and declined upstream changes is in [docs/upstream.md](docs/upstream.md); the
upstream changelog up to the fork point is preserved in the repository history.

notarx follows semantic versioning. Pre-1.0 releases are usable distributions
whose contracts can still evolve.
