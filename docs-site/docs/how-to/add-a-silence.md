# Silence alerts for a time window

Stop alertkube from dispatching matching alerts until a fixed point in time — either centrally from config, or per-workload via an annotation.

alertkube offers two ways to silence: an operator-controlled `silences:` block in the config file, and an `alert-silence-until` annotation that workload authors set on their own resources. Pick the one that matches who should hold the off switch.

## Method A — config `silences:` (operator-controlled)

Use this when an operator (someone with access to the ConfigMap) wants to mute alerts for a namespace, kind, or reason during planned maintenance.

1. Add a `silences:` entry to your `config.yaml`. Each entry needs `matchers` and an RFC3339 `until` timestamp:

    ```yaml
    silences:
      - matchers: {namespace: kube-system}
        until: "2026-06-15T00:00:00Z"
      - matchers: {kind: Pod, reason: CrashLoopBackOff, namespace: staging-.*}
        until: "2026-06-20T18:30:00Z"
    ```

2. Apply the change (the chart's checksum annotation triggers a rolling restart):

    ```bash
    helm upgrade alertkube ./helm --reuse-values -f config-values.yaml
    ```

!!! warning "`until` must be valid RFC3339"
    The config fails to load — and the controller refuses to start — if any `until` value is not parseable as RFC3339 (`silences[N]: until must be RFC3339`). Always include the timezone offset (`Z` for UTC). A silence whose `until` is in the past simply never matches; it does not error.

### Matcher semantics

Matchers compare alert fields against the values you give. Two keys are special:

- **`namespace` and `reason` are treated as anchored regexes** — the pattern is wrapped in `^...$`. So `namespace: prod-.*` matches `prod-api` but **not** `dev-prod-tools`. An invalid regex falls back to literal string equality.
- **All other keys** (`kind`, `severity`, `node`, `name`, …) are matched by **exact equality**.

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

Use this when a workload author wants to mute their own alerts during a deploy or known-flaky window, without touching shared config.

1. Annotate the resource (Deployment, Pod template, Job, …) with an RFC3339 timestamp:

    ```bash
    kubectl annotate deployment payments \
      alert-silence-until="2026-06-16T09:00:00Z" --overwrite
    ```

    ```yaml
    metadata:
      annotations:
        alert-silence-until: "2026-06-16T09:00:00Z"
    ```

2. Alerts for that resource are dropped until the timestamp passes. No restart of alertkube is required.

!!! note "Only real annotations count — not labels"
    alertkube reads the silence value from the resource's **annotations** only. Labels are commonly writable by lower-privilege automation, so they are deliberately not consulted (a label-writer could otherwise self-silence). Set the value as an annotation.

### Disabling annotation silences

Anyone who can `patch` a workload can otherwise silence its own alerts — that is fine for self-service teams, but unacceptable in environments where workload authors must not control alerting. Turn the annotation off cluster-wide:

```yaml
behavior:
  disableAnnotationSilences: true
```

When this is set, `alert-silence-until` annotations are ignored entirely. **Config-file `silences:` still apply**, because those are operator-controlled. This is the lever that decides whether silencing is self-service or operator-gated.

!!! info "Resolves are never silenced"
    A *resolved* alert always reaches the sinks that saw it fire, even if a matching silence is active. This prevents dangling PagerDuty/Opsgenie incidents — a silence suppresses noise, not closure.

## Verify

- **Config silence** — the controller logs config load on startup with no `silences[N]` error; trigger a matching condition and confirm no message is dispatched. The suppression is counted:

    ```bash
    curl -s localhost:9090/metrics | grep 'alertkube_alerts_suppressed_total{reason="silenced"}'
    ```

- **Annotation silence** — annotate a test workload with a near-future timestamp, trigger an alert, and confirm it is suppressed; the same `reason="silenced"` counter increments. With `disableAnnotationSilences: true`, the same test should dispatch normally.

## See also

- [Suppress dependent alerts with inhibitions](configure-inhibition.md) — silence by *cause*, not by time window.
- [Tune the mute window and storm folding](tune-mute-and-grouping.md) — reduce duplicate noise instead of silencing it.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md) — when to reach for which.
