# Configure Receiver and API Endpoints

alertkube exposes two HTTP endpoints on `metricsAddr`:

- **`POST /api/v1/receiver/alerts`** - Alertmanager webhook receiver (when enabled). Accepts Alertmanager webhook payloads and routes them through the same dedupe/grouping/routing/sink pipeline. Optional bearer auth.
- **`GET /api/v1/alerts`** - Read-only alerts API. JSON of active alerts plus recent history. Optional bearer auth.

Both return `503` until the controller installs their handlers.

## Enable the Receiver

```yaml
receiver:
  enabled: true
  allowAnonymous: false   # require a bearer token
```

If `receiver.enabled: true` and no token is configured, the controller refuses to start unless `allowAnonymous: true`. Use anonymous mode only behind NetworkPolicy or a firewall.

=== "Helm (via values)"
    ```yaml
    receiver:
      enabled: true
      token: "your-secret-token-here"
    ```

=== "Helm (via command line)"
    ```bash
    helm upgrade --install alertkube ... \
      --set receiver.enabled=true \
      --set receiver.token="your-secret-token-here"
    ```

The token is read on every request, so Secret rotation does not require a restart. Alertmanager must send `Authorization: Bearer <token>`.

Point Alertmanager at alertkube:

```yaml
# prometheus.yml or alertmanager.yml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ['alertmanager:9093']

# In alertmanager.yml:
route:
  receiver: 'alertkube'
receivers:
  - name: 'alertkube'
    webhook_configs:
      - url: 'http://alertkube.monitoring:9090/api/v1/receiver/alerts'
        send_resolved: true
        # If you set receiver.token:
        headers:
          Authorization: 'Bearer your-secret-token-here'
```

Receiver alerts use `kind: External`, so routing can treat them separately.

## Query `/api/v1/alerts`

`/api/v1/alerts` returns active alerts and recent history as JSON.

```bash
# Without authentication
curl http://localhost:9090/api/v1/alerts

# With bearer token authentication (if api.token is set)
curl -H "Authorization: Bearer your-secret-token-here" \
  http://localhost:9090/api/v1/alerts
```

Response shape:

```json
{
  "active": [
    {
      "fingerprint": "abc123def456",
      "kind": "Pod",
      "namespace": "default",
      "name": "web-server-xyz",
      "reason": "CrashLoopBackOff",
      "severity": "warning",
      "resolved": false,
      "lastFired": "2026-06-20T12:34:56Z",
      "labels": {
        "node": "node-1",
        "pod": "web-server-xyz"
      },
      "summary": "Pod default/web-server-xyz is CrashLoopBackOff",
      "details": "Container logs and event details..."
    }
  ],
  "recent": [
    {
      "fingerprint": "def789ghi012",
      "kind": "Node",
      "reason": "NodeNotReady",
      ...
    }
  ]
}
```

- **`active`** - alerts currently firing (unresolved).
- **`recent`** - recently resolved or muted alerts (historical window for correlation).

Protect it with a bearer token or NetworkPolicy. When `api.token` is empty, the endpoint is unauthenticated.

=== "Bearer token"
    ```yaml
    # config.yaml
    # OR helm values:
    api:
      token: "your-api-token"
    ```
    
    Then require the `Authorization: Bearer <token>` header on all requests.

## Config Reference

| Path | Type | Default | Description |
| --- | --- | --- | --- |
| `receiver.enabled` | bool | `false` | Enable the Alertmanager webhook receiver on `POST /api/v1/receiver/alerts`. |
| `receiver.allowAnonymous` | bool | `false` | Allow requests without a bearer token (only safe if the port is NetworkPolicy-locked). |
| `api.token` | string | `""` | Optional bearer token for `/api/v1/alerts` (empty = unauthenticated). |

Environment variables and Helm values:

| Env var | Helm value | Key | Notes |
| --- | --- | --- | --- |
| `ALERTKUBE_RECEIVER_TOKEN` | `receiver.token` / `receiver.tokenSecretKeyRef` | `receiverToken` | Bearer token for the receiver. Read on every request. |
| `ALERTKUBE_API_TOKEN` | `api.token` / `api.tokenSecretKeyRef` | `apiToken` | Bearer token for `/api/v1/alerts`. Read on every request. |

## Test an External Alert

```bash
# 1. Install alertkube with receiver enabled and a token
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --set receiver.enabled=true \
  --set receiver.token="my-secure-token" \
  --set cluster=my-cluster \
  --set slack.webhookUrl="https://hooks.slack.com/services/..."

# 2. Port-forward to the metrics port
kubectl port-forward svc/alertkube 9090:9090 &

# 3. Send a test Alertmanager webhook
curl -X POST http://localhost:9090/api/v1/receiver/alerts \
  -H "Authorization: Bearer my-secure-token" \
  -H "Content-Type: application/json" \
  -d '{
    "alerts": [
      {
        "status": "firing",
        "labels": {
          "severity": "critical",
          "alertname": "TestAlert"
        },
        "annotations": {
          "summary": "This is a test alert from external monitoring"
        }
      }
    ]
  }'

# 4. Query the API
curl -H "Authorization: Bearer my-api-token" \
  http://localhost:9090/api/v1/alerts | jq '.active[] | select(.kind == "External")'
```

## See Also

- [Configuration schema](../reference/config-schema.md) - full `receiver` and `api` block documentation.
- [Routing rules](../reference/config-schema.md#routing) - how to route receiver-sourced (`kind: External`) alerts to specific sinks.
- [Architecture](../architecture.md) - how the receiver fits into the alertkube pipeline.
