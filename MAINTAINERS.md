# Maintainers

Single source of truth for maintainers, reviewers, areas, and release process. Keep this in sync with [`.github/CODEOWNERS`](.github/CODEOWNERS).

## Current Maintainer

| Name | GitHub | Areas | Organization |
| --- | --- | --- | --- |
| Arya Soni | [@aryasoni98](https://github.com/aryasoni98) | all | Independent |

alertkube is seeking additional maintainers, ideally from a different organization. See the [contribution ladder](GOVERNANCE.md#contribution-ladder).

## Reviewers

Reviewers have review authority in specific areas but do not merge.

| Name | GitHub | Areas |
| --- | --- | --- |
| _(none yet)_ | | |

## Emeritus maintainers

| Name | GitHub |
| --- | --- |
| _(none yet)_ | |

## Areas

- `area/watchers` - `internal/watchers/` (Kubernetes resource observers)
- `area/sinks` - `internal/sinks/` (alert delivery targets)
- `area/router` - `internal/router/`, `internal/alert/` (suppression & dedup)
- `area/helm` - `helm/` (chart and Kubernetes manifests)
- `area/ci` - `.github/` (workflows, automation)
- `area/docs` - `docs/`, `docs-site/`, top-level docs

## Releasing

Releases are driven by [release-please](.github/workflows/release-please.yml) from [Conventional Commits](CONTRIBUTING.md#commit-messages).

**Primary (automated) flow:**

1. Merge Conventional-Commit PRs to `master`.
2. Review and merge the release-please Release PR.
3. The created `vX.Y.Z` tag triggers the release workflow: signed multi-arch image, SPDX SBOM, OCI Helm chart, and GitHub Release.
4. Verify the image, cosign signature, and chart install.

Manual fallback: bump `helm/Chart.yaml`, move `CHANGELOG.md` `[Unreleased]` under `vX.Y.Z`, create/push the tag, and update `.release-please-manifest.json`.

Releases use [Semantic Versioning](https://semver.org/). Breaking changes bump
the major version (or minor while `0.x`) and must be called out in the changelog.

## Becoming a maintainer

Contribute consistently, review pull requests, help triage/support users, then get nominated and confirmed per [governance](GOVERNANCE.md#contribution-ladder).
