# Release Process

How AlertKube versions, ships, and supports releases. For reporting a
vulnerability see [SECURITY.md](SECURITY.md).

## Versioning policy

[Semantic Versioning](https://semver.org/). The contract is broader than the Go
API, because almost nobody imports this as a library — they run it. A **major**
bump is required for a breaking change to any of:

| Surface | Breaking change looks like |
| --- | --- |
| Config schema | Removing or renaming a YAML key; tightening validation so a previously-valid config is rejected |
| Metric names | Renaming or removing a metric or label; changing a metric's type |
| HTTP API paths | Removing a route; changing a response shape; changing a status code's meaning |
| CRD schema | Removing a field; changing a field's type; a new required field |
| RBAC requirements | Needing a verb or resource the shipped Role does not already grant |
| Env vars | Removing one, or changing what an existing value means |
| Go module path / exported API | Any incompatible change |

Explicitly **not** breaking: adding a config key with a backward-compatible
default, adding a metric, adding a route, adding an optional CRD field,
changing log output, or changing internal package layout.

Adding a *new* alert reason is a minor. It can page someone who was not paged
before, so it is called out in the changelog.

## Support policy

| Version | Status | Receives |
| --- | --- | --- |
| Latest minor | Supported | Features, bug fixes, security fixes |
| Previous minor | Maintenance | Security fixes, critical bug fixes |
| Older | End of life | Nothing — upgrade |

Security fixes are backported to the two most recent minors. There is no LTS.
Pre-1.0 tags are unsupported.

## Release mechanics

Automated via [release-please](https://github.com/googleapis/release-please).

1. **Merge to `master`** using [Conventional Commits](https://www.conventionalcommits.org/).
   `feat:` → minor, `fix:` → patch, `feat!:`/`BREAKING CHANGE:` → major.
2. **release-please opens a release PR** accumulating the changelog and version
   bumps (`.release-please-manifest.json`, `helm/Chart.yaml`, the landing page,
   docs). CI's version-drift check fails if these disagree.
3. **Merge the release PR.** That tags `vX.Y.Z`.
4. **The tag triggers `release.yml`**: multi-arch image → GHCR, cosign
   signature, SBOM, and the Helm chart → `oci://ghcr.io/aryasoni98/charts`.
5. **Artifact Hub** picks up the chart via `artifacthub-repo.yml`.

Nothing is published by hand. If a step fails, fix forward — do not push tags
locally.

## Pre-release checklist

Before merging the release PR:

- [ ] `CHANGELOG.md` reads as user-facing changes, not commit subjects
- [ ] Breaking changes have a migration note with the exact commands or config
- [ ] `helm/Chart.yaml` `appVersion` matches the tag (CI drift check)
- [ ] Docs version references bumped (CI drift check)
- [ ] `just test` green, including `-race`
- [ ] `just lint` clean
- [ ] `just helm-lint` clean
- [ ] e2e (chainsaw) green against kind
- [ ] Trivy scan clean, or every finding has an accepted-risk note
- [ ] New metrics documented in `docs/docs/reference/metrics.md`
- [ ] New env vars documented in `docs/docs/reference/config-schema.md`
- [ ] Upgrade path verified from the previous minor, including state ConfigMap
      compatibility

## Emergency patch flow

For a security fix or a release-blocking regression:

1. Branch from the release tag: `git checkout -b release-vX.Y vX.Y.Z`
2. Cherry-pick the minimal fix. Resist bundling anything else — a patch release
   people apply under pressure should be reviewable in one sitting.
3. Open a PR against the release branch. Same required checks.
4. Tag `vX.Y.Z+1` from the release branch.
5. Forward-port to `master` if it did not originate there.

Embargoed security fixes are developed privately per
[SECURITY.md](SECURITY.md) and land as a single squashed commit at disclosure.

## Deprecation policy

A deprecated surface keeps working for **at least one full minor release**,
with a startup or response-level warning where possible, and a changelog entry
naming the replacement and the release it is removed in.

Example: the pre-v1 unversioned `/api/*` paths answer `308` redirects to their
`/api/v1/*` equivalents for one minor before removal.
