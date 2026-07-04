# Troubleshoot with Metrics

Use `/metrics` and the Grafana dashboard to answer four questions: did alerts fire, were they suppressed, did sinks receive them, and is dispatch backlogged?

For a complete metric reference, see [Metrics reference](../reference/metrics.md).

## Essential Metrics

| Metric | Query | Meaning | What to do if high |
| --- | --- | --- | --- |
| **Alert volume** | `alertkube_alerts_total` | Alerts emitted, by kind/severity/reason | Expected; use alerting rules to page if sustained |
| **Suppression** | `alertkube_alerts_suppressed_total` | Alerts dropped, by reason (dedupe/inhibited/silenced/grouped) | Expected; too high = tune mute window or grouping |
| **Active alerts** | `alertkube_active_alerts` | Count of currently firing, unresolved alerts | High during incidents; should drop after fixes |
| **Dispatch in-flight** | `alertkube_dispatch_inflight` | Sink sends currently queued on the rate limiter | Pinned high = storm is queueing; see rate-limiting section |
| **Sink send latency** | `alertkube_sink_send_seconds` | Time from dispatch to sink response, by sink and result | Expected; latency spikes = sink is slow |
| **Sink errors** | `alertkube_sink_errors_total` | Failed sends, by sink name | Rising = sink is down or credentials are stale |
| **Escalations** | `alertkube_escalations_total` | Alerts re-dispatched by escalation rules | Expected if escalations are configured |
| **Enrichment saturation** | `alertkube_enrichment_saturated_total` | Pod alerts shipped without logs because the enrichment pool was full | Rising = storm pressure; use grouping, raise `muteSeconds`, or disable log collection |

## Common Scenarios

### Nothing is alerting

**Symptom:** `alertkube_alerts_total` is zero or stuck, even when you trigger an alert condition.

Check:

1. Is the controller running?

    ```bash
    kubectl get pods -l app.kubernetes.io/name=alertkube
    ```

    If not, check logs for YAML syntax, invalid regex, unknown sinks, or validation failures:

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube --tail=100
    ```

2. Is the config mounted?

    ```bash
    kubectl exec -it deploy/alertkube -- cat /config/config.yaml
    ```

3. Are namespace filters excluding your test namespace?

    ```bash
    kubectl get pods -l app.kubernetes.io/name=alertkube -o jsonpath='{.items[0].spec.env[?(@.name=="WATCH_NAMESPACE")].value}'
    ```

4. Does the metrics endpoint expose `alertkube_*`?

    ```bash
    kubectl port-forward svc/alertkube 9090:9090 &
    curl http://localhost:9090/metrics | grep alertkube_
    ```

    No `alertkube_*` metrics usually means the HTTP server did not start.

### Alerts are firing but not reaching Slack/PagerDuty

**Symptom:** `alertkube_alerts_total` is rising, but nothing appears in the sink.

Check:

1. Is the sink being called?

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_sink_send_seconds_total
    ```

    Missing sink metrics usually mean no routing rule matched. `result="error"` means dispatch is failing.

2. Verify sink credentials:

    ```bash
    kubectl get secret alertkube -o jsonpath='{.data.slackWebhookUrl}' | base64 -d
    ```

    Check that the credential is set, valid, and reachable from the pod.

3. Check logs:

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube --tail=200 | grep -i error
    ```

4. Check routing:

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 20 routing:
    ```

    `reason` and `namespace` matchers are anchored regexes.

### Alerts are being suppressed unexpectedly

**Symptom:** You trigger an alert condition, but `alertkube_alerts_total` does not increase, or it increases but the sink does not receive it.

Check suppression counters:

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    ```

    The `reason` label tells you why:
    - **`dedupe` / `muted`** - same fingerprint fired recently (within `muteSeconds`). Wait, trigger from a fresh pod, or lower `muteSeconds` for testing.
    - **`silenced`** - a `silences:` config or `alert-silence-until` annotation matched.
    - **`inhibited`** - an inhibition rule suppressed it (e.g., pods on a down node).
    - **`grouped`** - it was the 2nd or later alert in a group within `windowSeconds`.

Then check the matching mechanism:

    ```bash
    kubectl get cm alertkube-config -o yaml | grep muteSeconds
    ```

- `dedupe` / `muted`: wait, use a fresh object, or lower `muteSeconds` for testing.
- `silenced`: inspect config silences and `alert-silence-until` annotations.
- `inhibited`: inspect source alerts and inhibition rules.
- `grouped`: check the grouping window and group fields.

### Sinks are getting rate-limited

**Symptom:** `alertkube_dispatch_inflight` pins high (e.g., stays at 20+ for minutes), and you see `alertkube_sink_errors_total` rising.

Identify the sink:

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_dispatch_inflight | sort -t'{' -k2
    ```

Raise that sink's rate or enable grouping:

    ```yaml
    sinkRates:
      pagerduty:
        perSecond: 20     # was 1
        burst: 50         # was 5
    ```

### Pod enrichment is being skipped

**Symptom:** `alertkube_enrichment_saturated_total` is rising, and pod alerts lack the `Container logs` block.

The enrichment worker pool is full. The alert still sends; only log/event enrichment is skipped. Reduce pressure with grouping, a longer `muteSeconds`, or `behavior.disableLogCollection: true`.

### Receiver is rejecting Alertmanager webhooks

**Symptom:** Alertmanager sends webhooks to `/api/v1/alerts`, but they are rejected with `401 Unauthorized` or `503 Service Unavailable`.

Check:

1. Is the receiver enabled?

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 2 receiver:
    ```

2. If a token is configured, Alertmanager must send `Authorization: Bearer <token>`.

    ```bash
    curl -X POST http://localhost:9090/api/v1/alerts \
      -H "Authorization: Bearer $(kubectl get secret alertkube -o jsonpath='{.data.receiverToken}' | base64 -d)" \
      -H "Content-Type: application/json" \
      -d '{"alerts": []}'
    ```

3. If you get `503`, the controller has not installed the handler yet; check pod logs and informer sync.

### High churn on resolved alerts

**Symptom:** `alertkube_alerts_total` with `severity=resolved` is rising rapidly, or PagerDuty incidents are closing and re-opening repeatedly.

`resolveTTLSeconds` is probably too short for the workload. It must be greater than 300 seconds and should usually be close to or above `muteSeconds`.

    ```bash
    kubectl get cm alertkube-config -o yaml | grep resolveTTLSeconds
    ```

## Health checks

### Is the controller ready to serve traffic?

```bash
curl -s http://localhost:9090/readyz
# Returns 200 OK once informer caches sync
# Returns 503 Service Unavailable while syncing or in follower mode (leader-election)
```

### Is the controller alive?

```bash
curl -s http://localhost:9090/healthz
# Always returns 200 OK once the HTTP server starts
```

### Are the API endpoints accessible?

```bash
# /api/alerts (read-only alerts introspection)
curl -s http://localhost:9090/api/alerts | jq .

# /api/v1/alerts (Alertmanager webhook receiver)
curl -X POST http://localhost:9090/api/v1/alerts \
  -H "Content-Type: application/json" \
  -d '{"alerts": []}'
```

## See Also

- [Metrics reference](../reference/metrics.md) - complete metric definitions and label values.
- [Troubleshooting (main docs)](https://github.com/aryasoni98/alertkube/blob/master/docs/TROUBLESHOOTING.md) - more detailed troubleshooting guide.
- [Operations guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) - capacity planning and SLOs.
- [Grafana dashboard](https://github.com/aryasoni98/alertkube/blob/master/docs/grafana-dashboard.json) - importable dashboard for visualization.
