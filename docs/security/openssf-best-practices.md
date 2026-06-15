# OpenSSF Best Practices — progress tracker

This tracks alertkube against the [OpenSSF Best Practices Badge](https://www.bestpractices.dev/)
criteria (passing level) and the [OpenSSF Scorecard](https://securityscorecards.dev/)
checks. The badge itself is registered and earned at bestpractices.dev — a
maintainer must create the project entry there (that step is account-gated).

> **Maintainer action:** register the project at
> <https://www.bestpractices.dev/>, then replace the placeholder badge in the
> README with the issued badge URL.

## Best Practices Badge — passing criteria

| Criterion | Status | Evidence |
| --- | --- | --- |
| Project website/README explains what it does | ✅ | `README.md` |
| OSS license (OSI) | ✅ | Apache-2.0, `LICENSE-2.0.txt` |
| License in standard location | ✅ | repo root |
| Contribution guide | ✅ | `CONTRIBUTING.md` |
| Code of Conduct | ✅ | `CODE_OF_CONDUCT.md` (CNCF CoC) |
| Public version-controlled source | ✅ | GitHub |
| Unique version numbering / semver | ✅ | tags `vX.Y.Z`, `CHANGELOG.md` |
| Release notes | ✅ | `CHANGELOG.md` + GitHub Releases |
| Bug/vuln reporting process | ✅ | `SECURITY.md`, GitHub Advisories |
| Private vulnerability reporting | ✅ | GitHub Security Advisories |
| Automated test suite | ✅ | `go test -race` in CI |
| Tests for new functionality (policy) | ✅ | `CONTRIBUTING.md` requires tests |
| Static analysis | ✅ | golangci-lint, CodeQL |
| Dynamic/fuzz analysis | ✅ | `go test -fuzz` targets (`internal/alert`, `internal/config`) |
| Signed releases | ✅ | cosign keyless signatures on images |
| SBOM | ✅ | SPDX SBOM per release |
| Dependencies monitored | ✅ | Dependabot |
| Two-factor auth on accounts | ⬜ | maintainer org/account setting |
| Earned the badge entry | ⬜ | register at bestpractices.dev |

## Scorecard checks

| Check | Status | Evidence |
| --- | --- | --- |
| Binary-Artifacts | ✅ | none committed |
| Branch-Protection | ⬜ | run `scripts/setup-branch-protection.sh` (admin) |
| CI-Tests | ✅ | `ci.yml` |
| CII-Best-Practices | ⬜ | pending badge registration |
| Code-Review | ⬜ | enforced once branch protection requires review |
| Dangerous-Workflow | ✅ | no `pull_request_target` + untrusted checkout |
| Dependency-Update-Tool | ✅ | Dependabot |
| Fuzzing | ✅ | native Go fuzz targets |
| License | ✅ | Apache-2.0 |
| Maintained | ✅ | active commits + releases |
| Packaging | ✅ | GHCR image + OCI Helm chart |
| Pinned-Dependencies | ✅ | all 65 workflow `uses:` are pinned to a 40-char commit SHA with a `# vX` comment; Dependabot keeps them current |
| SAST | ✅ | CodeQL + golangci-lint |
| Security-Policy | ✅ | `SECURITY.md` + `SECURITY-INSIGHTS.yml` |
| Signed-Releases | ✅ | cosign |
| Token-Permissions | ✅ | every workflow declares least-privilege `permissions` |
| Vulnerabilities | ✅ | Trivy + CodeQL gates |

## Known follow-ups

- Enable 2FA enforcement (org setting) and required code review (branch
  protection script) to close *Code-Review* and the 2FA criterion.
