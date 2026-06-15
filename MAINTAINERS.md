# Maintainers

This is the single source of truth for who maintains alertkube. It is kept in
sync with [`.github/CODEOWNERS`](.github/CODEOWNERS). Governance is described in
[`GOVERNANCE.md`](GOVERNANCE.md).

## Maintainers

| Name | GitHub | Areas | Organization |
| --- | --- | --- | --- |
| Arya Soni | [@aryasoni98](https://github.com/aryasoni98) | all | Independent |

> **Growing the team:** alertkube is actively seeking additional maintainers,
> ideally from a different organization, to satisfy the neutrality bar for
> foundation candidacy. If you have a track record with the project and want to
> help maintain it, open an issue or reach out to a current maintainer. See the
> contribution ladder in [`GOVERNANCE.md`](GOVERNANCE.md#contribution-ladder).

## Reviewers

Reviewers have review authority in specific areas but do not merge. None yet —
this is an open invitation. See [`GOVERNANCE.md`](GOVERNANCE.md#reviewer).

| Name | GitHub | Areas |
| --- | --- | --- |
| _(none yet)_ | | |

## Emeritus maintainers

People who were maintainers and have stepped down. We thank them for their work.

| Name | GitHub |
| --- | --- |
| _(none yet)_ | |

## Areas

`CODEOWNERS` maps these areas to reviewers/maintainers:

- `area/watchers` — `internal/watchers/` (Kubernetes resource observers)
- `area/sinks` — `internal/sinks/` (alert delivery targets)
- `area/router` — `internal/router/`, `internal/alert/` (suppression & dedup)
- `area/helm` — `helm/` (chart and Kubernetes manifests)
- `area/ci` — `.github/` (workflows, automation)
- `area/docs` — `docs/`, `docs-site/`, top-level docs

## Releasing

Releases are driven by [release-please](.github/workflows/release-please.yml),
fed by [Conventional Commits](CONTRIBUTING.md#commit-messages-conventional-commits).

**Primary (automated) flow:**

1. Merge Conventional-Commit PRs to `master` as normal. release-please keeps an
   open **"Release PR"** that bumps the version (per `feat`/`fix`/`!`) and updates
   `CHANGELOG.md`.
2. When ready to ship, review and **merge the Release PR**. That creates and
   pushes the `vX.Y.Z` tag.
3. The tag triggers the [`release` workflow](.github/workflows/release.yml):
   multi-arch image (cosign-signed), SPDX SBOM, OCI Helm chart, and the GitHub
   Release.
4. Verify: pull the image, `cosign verify` per the release notes, confirm the
   chart installs.

**Manual fallback** (if you must release without release-please): bump the chart
version in `helm/Chart.yaml`, move `CHANGELOG.md`'s `[Unreleased]` under a dated
`vX.Y.Z` heading, then `git tag -s vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z`
and update `.release-please-manifest.json` to match.

Releases use [Semantic Versioning](https://semver.org/). Breaking changes bump
the major version (or minor while `0.x`) and must be called out in the changelog.

## Becoming a maintainer

See the [contribution ladder](GOVERNANCE.md#contribution-ladder). In short:
contribute consistently, review pull requests, help triage and support users, then
get nominated by an existing maintainer and confirmed by the maintainer group.
