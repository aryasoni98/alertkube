# Contributing to alertkube

Thanks for your interest in contributing! This project follows a lightweight,
open governance model — see [`GOVERNANCE.md`](GOVERNANCE.md) and
[`MAINTAINERS.md`](MAINTAINERS.md).

## Where to start

- New here? See the curated [good first issues](docs/good-first-issues.md) and
  the GitHub issues labeled `good first issue` / `help wanted`.
- Have a question or an idea to float first? Use
  [GitHub Discussions](https://github.com/aryasoni98/alertkube/discussions).
- Found a security issue? Do **not** open a public issue — see [`SECURITY.md`](SECURITY.md).

## Getting started

```bash
git clone https://github.com/aryasoni98/alertkube.git
cd alertkube
go test ./...
go run .
```

Local dev with the stdout sink (no real cluster credentials needed for the sink):

```bash
export CLUSTER_NAME=local-dev
go run .
```

## Development workflow

1. Fork the repository and create a feature branch from `master`.
2. Make focused changes — one concern per PR.
3. Add or update tests for behavior changes.
4. Run `go test -race ./...` and `golangci-lint run` (CI pins v2.12.2).
5. Update `CHANGELOG.md` under `[Unreleased]` if the change is user-facing.
6. **Sign off** every commit (`git commit -s`) — see [DCO](#developer-certificate-of-origin-dco).
7. Open a pull request using the PR template.

## Code conventions

- Match existing naming and package layout under `internal/`.
- Watchers implement `Name() / Setup(ctx, factory, emit)` — see `internal/watchers/watcher.go`.
- Sinks implement `Name() / Send(ctx, alert) / Supports(severity)` — see `internal/sinks/sink.go`.
- Keep diffs minimal; avoid drive-by refactors.
- Security-sensitive paths (annotations, log redaction, credential handling) need tests.

## Adding a watcher

1. Create `internal/watchers/<kind>.go` (use `newSimple` if evaluation only needs the latest object state).
2. Register in `buildWatchers` in `builders.go`.
3. Add RBAC rules in `helm/templates/rbac.yaml` if a new API resource.
4. Add table-driven tests in `internal/watchers/<kind>_test.go`.

## Adding a sink

1. Create `internal/sinks/<name>.go`.
2. Register in `buildSinks` in `builders.go`.
3. Add Helm values and Secret wiring in `helm/`.
4. Document env vars in the README.

## Commit messages (Conventional Commits)

alertkube uses [Conventional Commits](https://www.conventionalcommits.org/). The
release tooling derives the changelog and the next version from commit types, so
this matters.

```
<type>(<optional scope>): <description>

[optional body]

[optional footer(s)]
Signed-off-by: Your Name <you@example.com>
```

Common types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`,
`build`, `perf`. Scopes mirror the [areas](MAINTAINERS.md#areas), e.g.
`feat(sinks): add Google Chat sink`. A breaking change uses `!`
(`feat(config)!: ...`) or a `BREAKING CHANGE:` footer.

## Developer Certificate of Origin (DCO)

Every commit must be signed off, certifying you wrote the patch or otherwise have
the right to submit it under the project's Apache-2.0 license. This is the
[Developer Certificate of Origin](https://developercertificate.org/) — there is
no separate CLA.

Sign off by adding a `Signed-off-by` trailer (Git does this for you):

```bash
git commit -s -m "fix(router): re-arm inhibition on muted source re-fire"
```

This appends:

```
Signed-off-by: Your Name <your.email@example.com>
```

The name/email must match your Git author identity. A CI check
([`dco.yml`](.github/workflows/dco.yml)) fails the PR if any commit lacks a
sign-off. To fix a branch retroactively:

```bash
git rebase --signoff origin/master
git push --force-with-lease
```

## Response times

This is a small project maintained by volunteers. We aim to give a **first
response to new issues and pull requests within 3 business days**. If yours has
gone quiet longer than that, a polite ping on the thread is welcome.

## Contributor ladder

Sustained, quality contributions can lead to reviewer and then maintainer status.
The path and criteria are in
[`GOVERNANCE.md`](GOVERNANCE.md#contribution-ladder). We are actively looking to
grow the maintainer team.

## Code of Conduct

This project follows the [CNCF Community Code of Conduct](CODE_OF_CONDUCT.md).
