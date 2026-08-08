# Repository settings checklist

These cannot be set from the repository contents — they are GitHub settings a
maintainer with admin rights must apply in the web UI or via `gh`. They are
tracked here so they are not silently forgotten.

## Branch protection (M-R6)

The repo runs 13 workflows. Workflows that are not **required** are advisory:
a PR can merge red. Verify at
`Settings → Branches → Branch protection rules → master`:

- [ ] Require a pull request before merging
- [ ] Require approvals: at least 1
- [ ] Dismiss stale approvals on new commits
- [ ] Require review from Code Owners (`.github/CODEOWNERS` exists)
- [ ] Require status checks to pass, and mark these **required**:
  - [ ] `Build & Test` (ci.yml)
  - [ ] `Fuzz (smoke)` (ci.yml)
  - [ ] `Docker build smoke test` (ci.yml)
  - [ ] `lint`
  - [ ] `codeql`
  - [ ] `dco`
  - [ ] `dependency-review`
  - [ ] `helm`
  - [ ] `e2e`
  - [ ] `trivy`
- [ ] Require branches to be up to date before merging
- [ ] Require conversation resolution
- [ ] Require signed commits
- [ ] Do not allow force pushes or deletions
- [ ] Include administrators

Verify from the CLI:

```bash
gh api repos/aryasoni98/alertkube/branches/master/protection \
  --jq '.required_status_checks.contexts'
```

## Community health (M-R8)

`Settings → General → Features`:

- [ ] Enable **Discussions**. Seed categories: Announcements, Q&A, Ideas,
      Show and tell. Move "how do I…" issues there so the issue tracker stays
      actionable work.
- [ ] Create a **project board** with columns Backlog / Next / In progress /
      Done, and link it from [ROADMAP.md](../../ROADMAP.md).
- [ ] Label good first issues with `good first issue` so GitHub surfaces them,
      and keep [docs/good-first-issues.md](../good-first-issues.md) in sync.
- [ ] Add topics: `kubernetes`, `alerting`, `observability`, `controller`,
      `sre`, `golang`, `prometheus`, `slack`, `pagerduty`.
- [ ] Set the repo description and website to the docs site.

## Security

- [ ] Enable **Private vulnerability reporting**
      (`Settings → Code security`), matching [SECURITY.md](../../SECURITY.md).
- [ ] Enable **Dependabot alerts** and **security updates** — the update PRs
      themselves are configured in [`.github/dependabot.yml`](../../.github/dependabot.yml).
- [ ] Enable **secret scanning** and **push protection**.
- [ ] Confirm the OpenSSF Scorecard badge reflects the new Dependabot config
      after its next run.

## Releases

- [ ] Confirm the `release-please` app or PAT can open PRs and push tags.
- [ ] Confirm GHCR publishing permissions for images and the Helm chart.
- [ ] Verify the Artifact Hub listing picks up `artifacthub-repo.yml`.
