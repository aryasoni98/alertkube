# CNCF readiness — status & handoff

Tracks the [ROADMAP](ROADMAP.md) Phases 0–2. Everything that is code, config,
CI, docs, or tests has been implemented and verified in-repo. The remaining items
are **human-gated**: they require an account, an org setting, a public action, or
another person, and cannot be done from the repository alone.

Legend: ✅ done in-repo · 🔶 partial (code done, action needed) · 👤 human-gated

## Phase 0 — Foundation

| Item | Status | Notes |
| --- | --- | --- |
| 0.1.1 CNCF Code of Conduct | ✅ | `CODE_OF_CONDUCT.md` (CNCF CoC v1.3 verbatim) |
| 0.1.2 Governance model | ✅ | `GOVERNANCE.md` |
| 0.1.3 Maintainer registry | ✅ | `MAINTAINERS.md` + `CODEOWNERS` synced |
| 0.1.4 Adopters seed | ✅ | `ADOPTERS.md` |
| 0.1.5 Decision log / ADRs | ✅ | `docs/decisions/` (3 ADRs + template) |
| 0.2.1 DCO | ✅ | `dco.yml` check + `CONTRIBUTING.md`; 👤 *optionally* install the DCO GitHub App too |
| 0.2.2 Issue templates | ✅ | `.github/ISSUE_TEMPLATE/` |
| 0.2.3 PR hygiene | ✅ | existing PR template already has the checklist |
| 0.2.4 Labels & triage | ✅ | `.github/labels.yml` + `labels.yml` workflow (runs on first push) |
| 0.2.5 Starter backlog | 🔶 | drafted in `docs/good-first-issues.md`; 👤 open them as GitHub issues |
| 0.2.6 Response SLA | ✅ | published in `CONTRIBUTING.md` |
| 0.3.1 Private vuln reporting | 👤 | enable in **Settings → Security** (SECURITY.md already points to Advisories) |
| 0.3.2 OpenSSF Scorecard | ✅ | `scorecard.yml` + README badge |
| 0.3.3 Branch protection | 🔶 | run `scripts/setup-branch-protection.sh` (needs admin) |
| 0.3.4 Action hardening | ✅ | least-privilege `permissions` on every workflow + all 65 `uses:` SHA-pinned (Dependabot keeps them current) |
| 0.3.5 OpenSSF best-practices badge | 👤 | register at bestpractices.dev (tracker: `docs/security/openssf-best-practices.md`) |
| 0.4 Docs site (MkDocs Material) | ✅ | `docs-site/`, 17 pages, `mkdocs build --strict` green; 👤 decide hosting vs the landing page (ADR-0002) |
| 0.4.7 README refresh | ✅ | badges + Community/Governance + docs links |
| 0.5.1 Conventional Commits | ✅ | documented in `CONTRIBUTING.md` |
| 0.5.2 release-please | ✅ | `release-please.yml` + config + manifest |
| 0.5.3 Release notes | ✅ | template already in `release.yml` |

## Phase 1 — Engineering maturity

| Item | Status | Notes |
| --- | --- | --- |
| 1.1.1 Coverage gate | ✅ | CI gate at 53% (actual 57.1%), ratchet documented; `internal/metrics` now covered (96.8%) |
| 1.1.2 Fuzz fingerprint | ✅ | `FuzzComputeFingerprint` (+ `FuzzMatchOrRegex`) — ran clean |
| 1.1.3 Fuzz config parse | ✅ | `FuzzLoad` — ran clean |
| 1.1.4 / 1.1.5 Mocks / envtest | ✅ | fake-clientset integration (per ADR-0001 we avoid controller-runtime/envtest); see `docs/TESTING.md` |
| 1.2.1 chainsaw scaffold | ✅ | `test/e2e/chainsaw/` |
| 1.2.2 kind version matrix | ✅ | `e2e.yml` (k8s 1.29/1.30/1.31) — runs in CI (not locally) |
| 1.2.3–1.2.6 Helm/sink/HA e2e | ✅ | smoke + HA jobs in `e2e.yml`, chart overrides verified via `helm template` |
| 1.3.1 client-go vs controller-runtime | ✅ | ADR-0001 |
| 1.3.2 CRD sketch | ✅ | `docs/design/crd-sketch.md` |
| 1.3.3 ConfigMap size audit | ✅ | `docs/design/configmap-size-audit.md` (measured ~605 B/alert) |
| 1.3.4 State backend ADR | ✅ | ADR-0003 |
| 1.4.1 Load harness | ✅ | `test/load/generate-pods.sh` |
| 1.4.3/1.4.4 Tuning docs | ✅ | `docs/PERFORMANCE.md` |
| 1.4.5 Benchmarks | ✅ | `internal/alert`, `internal/router` — ran clean |
| 1.5.1 golangci strict | ✅ | expanded set (+bodyclose, errorlint, noctx, nilerr, durationcheck, wastedassign, usestdlibvars, predeclared); runs clean on the tree |
| 1.5.3 pre-commit | ✅ | `.pre-commit-config.yaml` |

## Phase 2 — Distribution (codeable subset)

| Item | Status | Notes |
| --- | --- | --- |
| 2.1.2 chart-testing (ct) | ✅ | `ct lint` job + `.github/ct.yaml` |
| 2.1.3 kubeconform | ✅ | already in `helm.yml`, extended for PrometheusRule |
| 2.1.5 artifacthub-repo.yml | ✅ | present; 👤 paste the repository UUID after claiming |
| 2.1.6 Verified publisher | 👤 | claim the repo on Artifact Hub |
| 2.1.1 helm-docs | ✅ | `# --` annotations on every value + `helm/README.md.gotmpl`; `make helm-docs` regenerates; CI drift check in `helm.yml` |
| 2.2 Multi-arch / SBOM / SLSA | ✅ | image is multi-arch + cosign-signed + SPDX SBOM (existing `release.yml`) |
| 2.5.2 PrometheusRule | ✅ | `helm/templates/prometheusrule.yaml` (`prometheusRule.enabled`) |

## Phases 3–5 — what genuinely needs people

These are not code and are intentionally **not** done here:

- **3.3 Co-maintainer from a different employer** — the single biggest blocker
  for foundation neutrality. Start outreach now (see `GOVERNANCE.md`).
- **3.1 Community platforms** — enable GitHub Discussions; create Discord/Slack.
- **3.2.3 Hacktoberfest**, **3.5 Funding** (GitHub Sponsors / grants).
- **4.x Launch** — Show HN, blog posts, KubeCon CFP, adopter case studies.
- **5.x CNCF Sandbox application** — submit once ≥2 maintainers + ≥3 adopters.

## The short human to-do list

1. Run `scripts/setup-branch-protection.sh` (repo admin).
2. Enable private vulnerability reporting (Settings → Security).
3. Register the project at <https://www.bestpractices.dev/> and uncomment the
   README badge.
4. Claim the repo on Artifact Hub; paste the UUID into `artifacthub-repo.yml`.
5. Open the [good-first-issues](good-first-issues.md) as real issues.
6. Recruit a co-maintainer (different employer).
