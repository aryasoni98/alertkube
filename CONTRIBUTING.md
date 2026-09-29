# Contributing

## Where to start

- New here? See the curated [good first issues](docs/good-first-issues.md) and
  the GitHub issues labeled `good first issue` / `help wanted`.
- Have a question or an idea to float first? Use
  [GitHub Discussions](https://github.com/aryasoni98/alertkube/discussions).
- Found a security issue? Do **not** open a public issue - see [`SECURITY.md`](SECURITY.md).

## Local Setup

```bash
git clone https://github.com/aryasoni98/alertkube.git
cd alertkube
go test ./...
go run ./cmd/alertkube
```

## Workflow

1. Fork the repository and create a feature branch from `master`.
2. Make focused changes - one concern per PR.
3. Add or update tests for behavior changes.
4. Run `go test -race -count=1 ./...`, `golangci-lint run` (CI pins v2.13.2),
   and `govulncheck ./...` (v1.7.0). Run `just tools` to install the pinned tools.
5. Update `CHANGELOG.md` under `[Unreleased]` if the change is user-facing.
6. **Sign off** every commit (`git commit -s`) - see [DCO](#dco).
7. Open a pull request using the PR template.

## Code Conventions

- Match existing naming and package layout under `internal/`.
- Watchers implement `Name() / Setup(ctx, factory, emit)` - see `internal/watchers/watcher.go`.
- Sinks implement `Name() / Send(ctx, alert) / Supports(severity)` - see `internal/sinks/sink.go`.
- Keep diffs minimal; avoid drive-by refactors.
- Security-sensitive paths (annotations, log redaction, credential handling) need tests.

## Add a Watcher

1. Create `internal/watchers/<kind>.go` (use `newSimple` if evaluation only needs the latest object state).
2. Call `Register` from the watcher's `init` function, as in `internal/watchers/deployment.go`. The shared builder applies `Opts` at startup.
3. Add RBAC rules in `helm/templates/rbac.yaml` if a new API resource.
4. Add table-driven tests in `internal/watchers/<kind>_test.go`.

## Add a Sink

1. Create `internal/sinks/<name>.go`.
2. Call `Register` from the sink's `init` function, as in `internal/sinks/googlechat.go`. Reuse `chatWebhookSink` when only the payload format differs.
3. Add the routing name to `config.KnownSinks`; `TestKnownSinksMatchesRegistry` checks that validation and registration agree.
4. Declare each credential env var as a constant in `internal/sinks/cred.go`. Resolve it with `requireCred` (or set `chatWebhookSink.credEnv`) so a console credential override applies; a plain `os.Getenv` read, as for `SLACK_BOT_TOKEN` and `GENERIC_WEBHOOK_SECRET`, ignores overrides. If one credential fully drives a send, add the sink to `singleCredEnv` there so the console can test-fire it by Secret reference.
5. Add Helm values and Secret wiring in `helm/`, and document the environment variables.
6. Add payload and delivery tests, including escaping and credential handling where applicable.

## Commit Messages

alertkube uses [Conventional Commits](https://www.conventionalcommits.org/) for release-please changelog/versioning.

```text
<type>(<optional scope>): <description>

[optional body]

[optional footer(s)]
Signed-off-by: Your Name <you@example.com>
```

Common types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`, `perf`. Scopes mirror [code ownership areas](.github/CODEOWNERS), e.g. `feat(sinks): add Google Chat sink`. Breaking changes use `!` or a `BREAKING CHANGE:` footer.

## DCO

Every commit must be signed off under the [Developer Certificate of Origin](https://developercertificate.org/). There is no separate CLA.

```bash
git commit -s -m "fix(router): re-arm inhibition on muted source re-fire"
```

The trailer name/email must match your Git author identity. To fix a branch:

```bash
git rebase --signoff origin/master
git push --force-with-lease
```

## Response Times

This is a small volunteer-maintained project. We aim for first response within 3 business days; a polite ping is welcome after that.

## Contributor Ladder

Sustained contributions can lead to reviewer and maintainer status. See [`GOVERNANCE.md`](GOVERNANCE.md#contribution-ladder).

## Code of Conduct

This project follows the [CNCF Community Code of Conduct](CODE_OF_CONDUCT.md).
