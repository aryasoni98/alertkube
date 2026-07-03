# Good first issues

A curated backlog of small, well-scoped tasks for new contributors. Each is meant
to be roughly **one evening of work** and to touch a small, well-understood part
of the codebase. Pick one, comment on the tracking issue (or open one) to claim
it, and read [`CONTRIBUTING.md`](../CONTRIBUTING.md) first.

> Maintainers: these are seeds for GitHub issues labeled `good first issue` +
> `help wanted`. Open each as an issue and link it here. Verify scope against the
> current code before publishing - the codebase moves.

## Tooling & DX

1. **Cover `internal/env`.** This small env-parsing package is the one package at
   0% coverage. Add table-driven tests for its getters (defaults, overrides,
   malformed values). Scope: one `internal/env/env_test.go`.
2. **Add a `--version` flag.** Print version/commit/date (set via `-ldflags`) and
   exit. Scope: `main.go` flag parsing + a `release.yml` ldflags tweak.
   Hint: see `parseFlags()` in `main.go`.
3. **Add `examples/`** with 3 ready-to-use `config.yaml` samples (Slack-only,
   Slack+PagerDuty with escalations, multi-namespace routing). Scope: docs only.

## Sinks (follow the existing pattern in `internal/sinks/`)

4. **Add a Google Chat sink.** Implement the `Sink` interface
   (`Name()/Send()/Supports()`), reuse `httpx.PostJSON`, register in
   `buildSinks` (`builders.go`), add Helm values + secret wiring, document env
   vars. Hint: `internal/sinks/discord.go` is the closest template.
5. **Add a Mattermost sink** (Mattermost accepts Slack-compatible webhooks - this
   can be thin). Same checklist as above.

## Watchers (follow `internal/watchers/` + `newSimple`)

6. **Add a ReplicaSet watcher** for replica shortfall not owned by a Deployment.
   Scope: one `internal/watchers/replicaset.go` + table test + RBAC + register.
   Hint: `internal/watchers/statefulset.go` is the closest template.
7. **Add an Endpoints/Service "no ready endpoints" watcher.** Scope: one watcher +
   test + RBAC. Hint: use `newSimple[*corev1.Endpoints]`.

## Tests & quality

8. **Raise `internal/sinks` coverage.** It is the lowest-covered package. Add
   table-driven tests for an untested sink path (e.g. severity gating, error
   handling). Scope: one `_test.go`.
9. **Add a test for duplicate routing-rule detection** and, if missing, the
   validation in `config.Validate()` that warns on a routing match that can never
   be reached. Scope: `internal/config/`.

## Docs

10. **Port a doc page to the new MkDocs site** under `docs-site/docs/` (pick one
    from `docs/`). Scope: docs only. Hint: see `docs-site/mkdocs.yml` nav.
11. **Add a "Reference: metrics" page** enumerating every `alertkube_*` metric
    with its type and labels. Hint: grep `internal/metrics/metrics.go`.
12. **Add a Grafana dashboard panel** for `alertkube_dispatch_inflight` and
    document it. Scope: `docs/grafana-dashboard.json` + a doc note.
