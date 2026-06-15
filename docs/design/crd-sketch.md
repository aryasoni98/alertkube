# Design sketch: optional CRDs

**Status:** exploratory — no implementation. Records what alertkube's CRDs *would*
look like so the trade-off in [ADR-0001](../decisions/0001-client-go-over-controller-runtime.md)
can be revisited with a concrete picture rather than in the abstract.

## Why consider CRDs at all

Today routing, silences, and inhibitions live in one ConfigMap (`config.yaml`).
That is simple and operator-friendly, but it means:

- No per-object RBAC — anyone who can edit the ConfigMap edits everything.
- No `kubectl get silences` / status subresource / validation webhooks.
- GitOps diffs are a single blob, not per-rule objects.

CRDs would address those at the cost of a controller-runtime dependency and the
operational weight of CRD lifecycle management.

## Proposed resources

Three namespaced (or cluster-scoped) custom resources, mirroring today's config:

### `AlertRoute`

```yaml
apiVersion: alertkube.io/v1alpha1
kind: AlertRoute
metadata:
  name: critical-to-pager
spec:
  match:            # same semantics as today's routing match
    severity: critical
  sinks: [slack, pagerduty]
status:
  matchedLast24h: 42        # populated by the controller
  conditions: [...]
```

### `Silence`

```yaml
apiVersion: alertkube.io/v1alpha1
kind: Silence
metadata:
  name: kube-system-maintenance
spec:
  matchers:
    namespace: kube-system
  until: "2026-06-15T00:00:00Z"   # validated by an admission webhook
status:
  active: true
  expiresIn: "3h21m"
```

### `Inhibition`

```yaml
apiVersion: alertkube.io/v1alpha1
kind: Inhibition
metadata:
  name: node-down-silences-pods
spec:
  source: { kind: Node, reason: NodeNotReady }
  target: { kind: Pod }
  equal: [node]
  duration: 10m
status:
  active: 2          # currently-armed inhibition keys
```

## Field validation

OpenAPI schema validation (CEL rules) replaces today's `config.Validate()`:

- `spec.sinks` must be a subset of the registered sink names (enum or CEL).
- `spec.until` must be RFC3339 (format validation).
- `spec.duration` must parse as a Go duration (CEL regex).
- `spec.match`/`spec.matchers` must be non-empty (a severity override or route
  matching everything is almost always a mistake — today's validator already
  rejects the empty-match severity override).

## Migration path

1. Ship CRDs **additively**: the controller watches both the ConfigMap and the
   CRs, CRs taking precedence. No breaking change.
2. Provide a one-shot `config.yaml → CRs` converter (`alertkube migrate-config`).
3. Deprecate the ConfigMap routing block over two minor releases.

## What this would change architecturally

- Adopt `controller-runtime` + Kubebuilder; **supersede ADR-0001**.
- The watcher → store → router → sink pipeline is unaffected; only the *source of
  routing/silence/inhibition config* changes (ConfigMap → informer on CRs).
- `internal/router` would read its rules from a CR cache instead of a parsed
  struct; the matching logic (`alert.MatchLabels`) is reused as-is.

## Recommendation

Do **not** build this until there is concrete user demand for per-rule RBAC,
status, or GitOps-per-rule. It is a large surface for a benefit most single-team
deployments do not need. Revisit when an adopter hits the ConfigMap's limits
(governance or [size](configmap-size-audit.md)).
