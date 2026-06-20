# Troubleshoot alertkube using metrics

alertkube exposes rich Prometheus metrics that surface exactly what is happening: which alerts fired, which were suppressed and why, whether sinks are healthy, and whether storms are queueing. This guide teaches you to read the metrics dashboard and diagnose common issues.

For a complete metric reference, see [Metrics reference](../reference/metrics.md).

## The essential metrics to watch

Query these on your `/metrics` endpoint or import the [Grafana dashboard](https://github.com/aryasoni98/alertkube/blob/master/docs/grafana-dashboard.json):

| Metric | Query | Meaning | What to do if high |
| --- | --- | --- | --- |
| **Alert volume** | `alertkube_alerts_total` | Alerts emitted, by kind/severity/reason | Expected; use alerting rules to page if sustained |
| **Suppression** | `alertkube_alerts_suppressed_total` | Alerts dropped, by reason (dedupe/inhibited/silenced/grouped) | Expected; too high = tune mute window or grouping |
| **Active alerts** | `alertkube_active_alerts` | Count of currently firing, unresolved alerts | High during incidents; should drop after fixes |
| **Dispatch in-flight** | `alertkube_dispatch_inflight` | Sink sends currently queued on the rate limiter | Pinned high = storm is queueing; see rate-limiting section |
| **Sink send latency** | `alertkube_sink_send_seconds` | Time from dispatch to sink response, by sink and result | Expected; latency spikes = sink is slow |
| **Sink errors** | `alertkube_sink_errors_total` | Failed sends, by sink name | Rising = sink is down or credentials are stale |
| **Escalations** | `alertkube_escalations_total` | Alerts re-dispatched by escalation rules | Expected if escalations are configured |
| **Enrichment saturation** | `alertkube_enrichment_saturated_total` | Pod alerts shipped without events/logs because the enrichment pool (fixed at 4) was full | Rising = storm pressure; fold with grouping, raise `muteSeconds`, or `disableLogCollection` (pool size is not a runtime knob) |

## Common scenarios

### Nothing is alerting

**Symptom:** `alertkube_alerts_total` is zero or stuck, even when you trigger an alert condition.

**Diagnosis:**

1. **Is the controller healthy?**

    ```bash
    kubectl get pods -l app.kubernetes.io/name=alertkube
    ```

    If not `Running`, check logs:

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube --tail=100
    ```

    Look for config load errors (YAML syntax, invalid regex, unknown sinks, validation failures like `muteSeconds` not exceeding 300).

2. **Is the config mounted?**

    ```bash
    kubectl exec -it deploy/alertkube -- cat /config/config.yaml
    ```

    If the file is empty or missing, the ConfigMap mount is wrong.

3. **Are namespaces being watched?**

    Check the namespace filters:

    ```bash
    kubectl get pods -l app.kubernetes.io/name=alertkube -o jsonpath='{.items[0].spec.env[?(@.name=="WATCH_NAMESPACE")].value}'
    ```

    If `filters.watchedNamespaces` is set to a regex that excludes your test namespace, you will see no alerts.

4. **Query the metrics endpoint:**

    ```bash
    kubectl port-forward svc/alertkube 9090:9090 &
    curl http://localhost:9090/metrics | grep alertkube_
    ```

    If you see metrics other than `alertkube_alerts_total` (e.g., `alertkube_active_alerts`, `alertkube_sink_send_seconds`), the controller is running and the metric server is up. If you see no `alertkube_*` metrics at all, the controller has not started the metric server.

### Alerts are firing but not reaching Slack/PagerDuty

**Symptom:** `alertkube_alerts_total` is rising, but nothing appears in the sink.

**Diagnosis:**

1. **Are alerts being routed?**

    Query the routing counters:

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_sink_send_seconds_total
    ```

    If `alertkube_sink_send_seconds` for your sink is absent or has result `error`, the dispatch is failing.

2. **Check sink credentials:**

    ```bash
    kubectl get secret alertkube -o jsonpath='{.data.slackWebhookUrl}' | base64 -d
    ```

    Verify:
    - The credential is actually set (not empty).
    - For Slack bot tokens, the token is valid and the bot is invited to each channel.
    - For PagerDuty, the routing key is valid and not expired.
    - For custom webhooks, the endpoint is reachable from the pod.

3. **Check the logs for send errors:**

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube --tail=200 | grep -i error
    ```

    Look for messages like `sink send failed`, `authentication failed`, `connection refused`.

4. **Check your routing rules:**

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 20 routing:
    ```

    Confirm your alert's severity/kind/namespace matches a routing rule. If no rule matches, the alert is dropped (not sent to any sink). Use `reason` matchers carefully — they are anchored regexes.

### Alerts are being suppressed unexpectedly

**Symptom:** You trigger an alert condition, but `alertkube_alerts_total` does not increase, or it increases but the sink does not receive it.

**Diagnosis:**

1. **Check the suppression counters:**

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    ```

    The `reason` label tells you why:
    - **`dedupe` / `muted`** — same fingerprint fired recently (within `muteSeconds`). Wait, trigger from a fresh pod, or lower `muteSeconds` for testing.
    - **`silenced`** — a `silences:` config or `alert-silence-until` annotation matched.
    - **`inhibited`** — an inhibition rule suppressed it (e.g., pods on a down node).
    - **`grouped`** — it was the 2nd or later alert in a group within `windowSeconds`.

2. **Verify your mute window:**

    ```bash
    kubectl get cm alertkube-config -o yaml | grep muteSeconds
    ```

    If testing and the alert fired within `muteSeconds` of the last fire, it will be muted. Trigger from a *different* pod, or temporarily lower the mute window.

3. **Check for active silences:**

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 10 silences:
    ```

    Confirm no `silences:` rule matches your test alert. Check the `until` timestamp — if it is in the future, the silence is active.

4. **Check for annotations:**

    ```bash
    kubectl get pod <name> -o jsonpath='{.metadata.annotations.alert-silence-until}'
    ```

    If this is set and in the future, the alert is silenced by annotation. Either clear it or disable annotation silences entirely if that is policy.

5. **Check for inhibitions:**

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 10 inhibitions:
    ```

    Is there an active source alert? If a `NodeNotReady` is firing, for example, all pods on that node will be inhibited. Resolve the source first, or adjust the inhibition `duration`.

### Sinks are getting rate-limited

**Symptom:** `alertkube_dispatch_inflight` pins high (e.g., stays at 20+ for minutes), and you see `alertkube_sink_errors_total` rising.

**Diagnosis:**

1. **Identify the bottleneck:**

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_dispatch_inflight | sort -t'{' -k2
    ```

    Which sink has the highest in-flight count? That is your bottleneck.

2. **Check the sink send latency:**

    ```bash
    curl http://localhost:9090/metrics | grep alertkube_sink_send_seconds | grep <sink_name>
    ```

    If `_count` is high but `_sum` (total latency) is very high, the sink is slow. If `_count` is low, the sink is backlogged.

3. **Is the sink down?**

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube | grep -i <sink_name>
    ```

    Look for `connection refused`, `timeout`, or `authentication failed`.

4. **Increase the per-sink rate limit:**

    ```yaml
    sinkRates:
      pagerduty:
        perSecond: 20     # was 1
        burst: 50         # was 5
    ```

    Or enable grouping to fold storms:

    ```yaml
    grouping:
      enabled: true
      windowSeconds: 30
    ```

### Pod enrichment is being skipped

**Symptom:** `alertkube_enrichment_saturated_total` is rising, and pod alerts lack the `Container logs` block.

**Diagnosis:**

1. **The enrichment pool is full.** alertkube fetches events and previous-container
   logs for pod alerts through a bounded worker pool (`enrichWorkers`, a
   compile-time constant of `4` in `internal/watchers/pod.go`). Under sustained
   pod-alert storms the pool saturates; rather than block dispatch, alertkube
   ships the alert **without** events/logs and increments the counter. The alert
   still fires — only the enrichment is dropped — so a rising counter is a
   capacity signal, not a failure.

2. **This is not a runtime knob.** The pool size is not exposed as a flag, env
   var, or Helm value — changing it requires editing the constant and rebuilding
   the image. (A configurable pool is a reasonable contribution; see the
   [roadmap](https://github.com/aryasoni98/alertkube/blob/master/docs/ROADMAP.md).)

3. **Reduce enrichment pressure instead.** Lower the rate of pod alerts that need
   enrichment, or the per-alert enrichment cost:

    - Enable [storm folding](tune-mute-and-grouping.md) so a burst of same-group
      pod alerts collapses into one summary instead of N enriched alerts.
    - Raise `behavior.muteSeconds` so a flapping pod re-enriches less often.
    - Set `behavior.disableLogCollection: true` to skip the log fetch entirely
      (events are cheaper than logs); enrichment then rarely saturates.

### Receiver is rejecting Alertmanager webhooks

**Symptom:** Alertmanager sends webhooks to `/api/v1/alerts`, but they are rejected with `401 Unauthorized` or `503 Service Unavailable`.

**Diagnosis:**

1. **Is the receiver enabled?**

    ```bash
    kubectl get cm alertkube-config -o yaml | grep -A 2 receiver:
    ```

    If `receiver.enabled: false`, the endpoint is disabled. Enable it and restart.

2. **Is the bearer token correct?**

    If `receiver.enabled: true` and a token is set, Alertmanager must send `Authorization: Bearer <token>` on every POST.

    ```bash
    curl -X POST http://localhost:9090/api/v1/alerts \
      -H "Authorization: Bearer $(kubectl get secret alertkube -o jsonpath='{.data.receiverToken}' | base64 -d)" \
      -H "Content-Type: application/json" \
      -d '{"alerts": []}'
    ```

    If you get `200 OK`, the receiver is healthy.

3. **Is the controller started?**

    The receiver handler is only installed once the controller starts (after informer sync). If the pod is in `CrashLoopBackOff` or stuck initializing, the receiver returns `503` even when `enabled: true`.

    ```bash
    kubectl logs -l app.kubernetes.io/name=alertkube | tail -20
    ```

    Look for informer sync messages and any errors during controller startup.

### High churn on resolved alerts

**Symptom:** `alertkube_alerts_total` with `severity=resolved` is rising rapidly, or PagerDuty incidents are closing and re-opening repeatedly.

**Diagnosis:**

1. **The resolve TTL is too short.** If `resolveTTLSeconds` is set to a value that is too close to the mute window, a still-firing-but-muted condition may look resolved, then re-fire immediately.

    ```bash
    kubectl get cm alertkube-config -o yaml | grep resolveTTLSeconds
    ```

    Set it higher (e.g., 2–3x the mute window) or slightly higher than mute window if you want fast resolves.

2. **Informer resync is firing resolves.** The controller resyncs every 300 seconds, re-delivering every cached object as an Update. If you have no current fires after resync, a resolve may emit. This is usually fine, but if you see churning, increase the mute window or disable the resolve TTL (set to a very high value).

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

## See also

- [Metrics reference](../reference/metrics.md) — complete metric definitions and label values.
- [Troubleshooting (main docs)](https://github.com/aryasoni98/alertkube/blob/master/docs/TROUBLESHOOTING.md) — more detailed troubleshooting guide.
- [Operations guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) — capacity planning and SLOs.
- [Grafana dashboard](https://github.com/aryasoni98/alertkube/blob/master/docs/grafana-dashboard.json) — importable dashboard for visualization.
