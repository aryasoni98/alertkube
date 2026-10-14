# Upgrade from v1 to v2

Version 2 keeps the controller's watchers, cloud sources, notification sinks,
HTTP API, and ConfigMap state backend. It tightens configuration validation and
changes alert identity and Silence CR scope. Validate the actual configuration
before changing a running release.

## Validate configuration

Go users must adopt the v2 module path. Install the command with
`go install github.com/aryasoni98/alertkube/v2/cmd/alertkube@v2.0.0`, and import
public Silence types from `github.com/aryasoni98/alertkube/v2/api/v1alpha1`.
The v1 module remains available at its existing tags. This follows
[Go's major-version module rules](https://go.dev/doc/modules/major-version).

With a v2 binary, run:

```sh
alertkube validate --config config.yaml
```

Validation applies the process's environment defaults. Use the same non-secret
environment values as the controller, including `POD_NAMESPACE` when persistence
is enabled. The command does not contact Kubernetes or notification services.

- Remove unknown YAML keys and extra YAML documents. Helm values are a different
  schema: pass values to Helm, and validate the `config.yaml` it renders.
- Put a catch-all route (`match: {}`) last. Give silences, inhibitions, maintenance
  windows, and escalations selective matchers. Empty label values and invalid
  namespace/reason regular expressions are rejected. Broad patterns such as
  `.*` are rejected except on the final route.
- Namespace and pod-name filter regular expressions now match from the start.
  Write `.*prod.*` if a substring filter is intentional.
- Explicit YAML zero/false values, including aliases and merges, win over
  environment fallbacks. Remove a key to opt into its environment default;
  ensure numeric values satisfy validation.
- Keep `correlation.enabled` false: topology correlation is not implemented.
- Remove `slack.username` from Helm values; it never affected rendered config.

## Preserve state and plan incident notifications

Keep the existing state ConfigMap name and namespace. Snapshot format version 1
remains supported, including active alerts, runtime silences, and pending
deliveries. New escalation marks are additive. Do not delete the state ConfigMap
as part of this upgrade.

Internal fingerprints now use unambiguous, length-prefixed fields and 16 hex
characters. Received Alertmanager fingerprints use an `am-` prefix. Existing
stored fingerprints remain opaque identifiers: they restore and resolve with
their old identity, while new observations use the new identity. A standing
condition can therefore open a new incident once after upgrade, with the old
incident resolving when its stored TTL expires. Plan the rollout with responders
and check PagerDuty/Opsgenie for the expected transition. This is not an
in-place incident-key migration.

## Review Silence CRs

Namespaced Silence CRs now affect only their own Kubernetes namespace. An omitted
namespace matcher is filled in; a different namespace or namespace pattern makes
the CR invalid. Move a CR to the namespace it should silence. Each CR expires no
later than 30 days after creation, so recreate it to renew a longer silence.

Use config-file or runtime-API silences for Nodes, cloud resources, or other
alerts without a matching Kubernetes namespace. Cluster-scoped CRDs are opt-in;
changing an existing CRD's scope requires recreating it and its CRs. The ordinary
v2 upgrade does not require this destructive scope change.

## Render and upgrade the chart

Review the full rendered chart before installing:

```sh
helm template alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --version 2.0.0 --namespace alertkube -f values.yaml > rendered.yaml

helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --version 2.0.0 --namespace alertkube -f values.yaml --wait --timeout 5m
```

Configure read-API authentication or an appropriate NetworkPolicy; the chart
requires explicit acceptance before exposing unauthenticated reads. Preserve
existing sink Secrets and persistence values. Keep the default 45-second
termination grace period or a longer one so the controller can drain and save.

After rollout, verify readiness, the logged binary version, sink error metrics,
active alerts, and a firing/resolved test notification. Keep the previous Helm
revision available for rollback; rolling back also changes live fingerprint
identities, so check outstanding incidents again.
