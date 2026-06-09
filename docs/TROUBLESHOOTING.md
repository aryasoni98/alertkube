# alertkube — Troubleshooting

Symptom → root cause → fix. Pair with [`OPERATIONS.md`](OPERATIONS.md) and [`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md).

---

## Pod crashloops at startup

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `klog.Fatalf("kube client init …")` after 30 s | Missing or broken kubeconfig; SA can't reach apiserver | Check `serviceAccountName` + RBAC. `kubectl auth can-i list pods --as=system:serviceaccount:<ns>:alertkube`. |
| `informer cache for … did not sync` | RBAC denies one of the watched resources | Re-apply `helm/templates/rbac.yaml`; ensure `pods/log`, `events`, and apps/batch verbs are granted. |
| `cluster is required` | Helm value empty | `--set cluster=<name>` or set in your values file. |
| OOMKill after a minute | Pod cache exceeds limit | Raise `resources.limits.memory` to `1Gi`+ or scope down with `filters.watchedNamespaces`. |

## `/readyz` keeps returning 503

The atomic ready flag is only flipped after `factory.WaitForCacheSync`. If 503 persists:

1. `kubectl logs deploy/alertkube` — look for `alertkube started` (synced) or `informer cache for X did not sync` (RBAC).
2. `kubectl describe pod alertkube-*` — check the readiness probe failure reason.
3. Confirm the apiserver is reachable from the pod (`kubectl exec -it deploy/alertkube -- wget -qO- https://kubernetes.default/healthz`). On a hardened NetworkPolicy install, add an egress rule for the apiserver.

## Alerts firing in Slack but not PagerDuty

| Cause | Diagnostic | Fix |
| --- | --- | --- |
| `PAGERDUTY_ROUTING_KEY` empty | `kubectl exec deploy/alertkube -- printenv PAGERDUTY_ROUTING_KEY` | Set `pagerduty.routingKey` or `pagerduty.routingKeySecretKeyRef`. |
| Severity gate | PagerDuty sink only sends `critical`. | If you need pageable warnings, override `Supports` (will need a code change). |
| Routing rule | First-match-wins. A broader rule may swallow the alert before the PagerDuty rule. | Reorder `routing:` in config so `pagerduty` rule comes first or anchor `match:` more tightly. |
| Sink error | `alertkube_sink_errors_total{sink="pagerduty"} > 0` | Inspect logs; PagerDuty Events API returns the failing field name in the response. |

## Alerts firing in unexpected Slack channel

The channel resolution order:

1. `alert-slack-channel` annotation on the pod — IF it matches `^#?[a-z0-9._-]{1,80}$`.
2. The severity-mapped channel from `channels.{critical,warning,info}`.
3. The `warning` channel as a fallback.

If the override regex rejects a value you see `ignoring invalid alert-slack-channel override for <fingerprint>` in logs. Otherwise check the pod's annotations.

## Tenant abuse: workload silencing its own alerts

The `alert-silence-until` annotation works by design. To stop accepting it from workloads:

1. (Quick) Add a Kyverno / OPA policy denying `alert-silence-until` outside an admin allow-list of namespaces.
2. (Tracked) Move silences fully into operator-controlled config — open audit item `security #3`.

## Resolved alerts never fire

Resolution depends on `EndsAt`. Triage:

1. Confirm the resolve TTL is reasonable (`behavior.resolveTTLSeconds`, default 600).
2. Check that the resolve happens *after* the workload stops emitting events. If the same alert keeps re-firing, `Touch()` is pushing `EndsAt` forward indefinitely — that's correct behavior while the condition persists.
3. Inspect `kubectl logs ... | grep RESOLVED` — the synthetic resolved Slack message logs as `RESOLVED: <kind> <reason>`. PagerDuty closes the incident via dedupKey.

## Sink errors after a Slack rotation

You rotated the webhook URL but alerts still fail with `POST https://hooks.slack.com/[REDACTED] returned 404`. Sinks load env at process start.

```
kubectl rollout restart deploy/alertkube
```

is required after secret rotation (tracked as `code_quality #7`).

## Sink errors but no detail

`alertkube_sink_errors_total{sink="..."}` increments but logs are quiet. Bump verbosity:

```
kubectl set env deploy/alertkube GODEBUG=netdns=go
# then in args:
helm upgrade ... --set 'extraArgs={-v=4}'
```

The HTTP path additionally logs `sink "X" send failed: …` at default verbosity once retry exhaustion is reached.

## Logs contain `[REDACTED]`

By design. `collectors.RedactSecrets` masks AWS / GitHub / Slack / OpenAI / Bearer / `password|secret|token=` / URL query tokens before logs are attached to alerts. To debug a redaction false-positive, fetch the raw container log directly:

```
kubectl logs <workload-pod> --previous --tail=50
```

If the redactor is dropping legitimate output, file an issue with the masked + unmasked sample (rotate any incidentally exposed secret afterward).

## CPU pegged at 100 %

Usually the result of:

- Massive ConfigMap with thousands of routing/inhibition rules (rebuild on every alert).
- A regex in `filters.watchedNamespaces` that is catastrophic-backtracking.

Mitigation:

- Simplify routing.
- Use literal prefixes in filters where possible.
- The runtime caches anchored regex from `alert.MatchLabels` — but `filter.Set` re-evaluates per token.

## Where to file bugs

Open an issue at https://github.com/aryasoni98/alertkube/issues with:

- `kubectl version --short`
- `helm get values alertkube`
- `kubectl logs deploy/alertkube --tail=200`
- `curl localhost:9090/metrics` for any `alertkube_*` line that's non-zero or zero where you expect otherwise.
