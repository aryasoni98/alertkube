# Maintainers

alertkube is maintained by the people listed in
[`.github/CODEOWNERS`](.github/CODEOWNERS). How someone becomes a maintainer,
and how one steps down, is in [`GOVERNANCE.md`](GOVERNANCE.md).

## Areas

<a id="areas"></a>

Review routing follows the `area/*` comments in CODEOWNERS:

| Area | Path |
| --- | --- |
| helm | `helm/` |
| sinks | `internal/sinks/` |
| watchers | `internal/watchers/` |
| router | `internal/router/`, `internal/alert/` |
| ci | `.github/` |
| docs | `docs/`, `web/` |

A reviewer approval is advisory. A maintainer merges.

## Releasing

<a id="releasing"></a>

Releases are cut from `master` by a maintainer:

1. `CHANGELOG.md` has an entry for the version.
2. CI on the release commit is green, including the signed-off-by check.
3. Tag `vX.Y.Z`. The release workflow builds the image, the Helm chart, and the SBOM.
4. Confirm the chart and image published under `ghcr.io/aryasoni98`.

Do not force-push a release tag.
