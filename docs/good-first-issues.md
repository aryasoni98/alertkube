# Good first issues

A curated backlog of small, well-scoped tasks for new contributors. Each is meant
to be roughly **one evening of work**. Pick one, comment on the tracking issue
(or open one) to claim it, and read [`CONTRIBUTING.md`](../CONTRIBUTING.md) first.

Shipped and removed from this list: `internal/env` tests, `--version`,
`examples/`, the Google Chat and Mattermost sinks, the metrics reference page,
and the `alertkube_dispatch_inflight` Grafana panel.

## Watchers

1. **Add a ReplicaSet watcher** for replica shortfall not owned by a Deployment.
   Scope: one `internal/watchers/replicaset.go` + table test + RBAC + register.
   Hint: `internal/watchers/statefulset.go` is the closest template. Watchers
   self-register from `init()`.
2. **Add a Service "no ready endpoints" watcher on EndpointSlices.** Scope: one
   watcher + test + RBAC. Hint: use `newSimple[*discoveryv1.EndpointSlice]`; the
   core `Endpoints` API is deprecated since Kubernetes v1.33. A Service can own
   several slices (label `kubernetes.io/service-name`).

## Tests

3. **Add a sink test for an untested error path** (severity gating or a non-2xx
   response). Scope: one `_test.go` beside the sink. Sinks self-register in
   `init()`; there is no `buildSinks` list to edit.
