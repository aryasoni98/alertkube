# Contributing to alertkube

Thanks for your interest in contributing!

## Getting started

```bash
git clone https://github.com/aryasoni98/alertkube.git
cd alertkube
go test ./...
go run .
```

Local dev with stdout sink:

```bash
export CLUSTER_NAME=local-dev
go run .
```

## Development workflow

1. Fork the repository and create a feature branch from `main`.
2. Make focused changes — one concern per PR.
3. Add or update tests for behavior changes.
4. Run `go test ./...` and `golangci-lint run` (CI uses v1.64.8).
5. Update `CHANGELOG.md` under `[Unreleased]` if user-facing.
6. Open a pull request using the PR template.

## Code conventions

- Match existing naming and package layout under `internal/`.
- Watchers implement `Name() / Setup(ctx, factory, emit)` — see `internal/watchers/watcher.go`.
- Sinks implement `Name() / Send(ctx, alert) / Supports(severity)` — see `internal/sinks/sink.go`.
- Keep diffs minimal; avoid drive-by refactors.
- Security-sensitive paths (annotations, log redaction, credential handling) need tests.

## Adding a watcher

1. Create `internal/watchers/<kind>.go`.
2. Register in `main.go`.
3. Add RBAC rules in `helm/templates/rbac.yaml` if a new API resource.
4. Add table-driven tests in `internal/watchers/<kind>_test.go`.

## Adding a sink

1. Create `internal/sinks/<name>.go`.
2. Register in `main.go` sink registry.
3. Add Helm values and Secret wiring in `helm/`.
4. Document env vars in README.

## DCO

By contributing, you agree that your contributions are licensed under the Apache-2.0 license and that you have the right to submit them.

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).

## Code of Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
