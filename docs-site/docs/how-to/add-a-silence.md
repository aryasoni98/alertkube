# Silence Alerts

Silence alerts either centrally in config or per workload with an annotation.

## Method A — config `silences:` (operator-controlled)

Use config silences for operator-controlled maintenance windows:

    ```yaml
    silences:
      - matchers: {namespace: kube-system}
        until: "2026-06-15T00:00:00Z"
      - matchers: {kind: Pod, reason: CrashLoopBackOff, namespace: staging-.*}
        until: "2026-06-20T18:30:00Z"
    ```

Apply the change:

    ```bash
    helm upgrade alertkube ./helm --reuse-values -f config-values.yaml
    ```

`until` must be RFC3339. Invalid timestamps fail config load; past timestamps simply do not match.

### Matcher semantics

`namespace` and `reason` are anchored regexes. All other keys, such as `kind`, `severity`, `node`, and `name`, are exact matches.

```yaml
silences:
  # Anchored regex: every prod-* namespace, but not staging-prod.
  - matchers: {namespace: prod-.*}
    until: "2026-07-01T00:00:00Z"
  # Exact match on kind + severity; regex on reason.
  - matchers: {kind: Node, severity: warning, reason: .*Pressure}
    until: "2026-07-01T00:00:00Z"
```

## Method B — the `alert-silence-until` annotation (workload self-service)

Use the annotation for workload self-service:

    ```bash
    kubectl annotate deployment payments \
      alert-silence-until="2026-06-16T09:00:00Z" --overwrite
    ```

    ```yaml
    metadata:
      annotations:
        alert-silence-until: "2026-06-16T09:00:00Z"
    ```

Labels do not count. The value must be a real annotation.

### Disabling annotation silences

Disable workload self-silencing:

```yaml
behavior:
  disableAnnotationSilences: true
```

Config-file silences still apply. Resolved alerts are never silenced, so incidents can close.

## Verify

- Config silence: trigger a matching condition and confirm no message is dispatched. The suppression is counted:

    ```bash
    curl -s localhost:9090/metrics | grep 'alertkube_alerts_suppressed_total{reason="silenced"}'
    ```

- Annotation silence: annotate a test workload, trigger an alert, and confirm `reason="silenced"` increments. With annotation silences disabled, the same test should dispatch.

## See Also

- [Suppress dependent alerts with inhibitions](configure-inhibition.md) — silence by *cause*, not by time window.
- [Tune the mute window and storm folding](tune-mute-and-grouping.md) — reduce duplicate noise instead of silencing it.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md) — when to reach for which.
