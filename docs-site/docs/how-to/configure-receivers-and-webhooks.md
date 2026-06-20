# Configure Alertmanager webhook receiver and API endpoints

alertkube exposes two HTTP API endpoints on the metrics address for receiving alerts and introspecting state:

- **`POST /api/v1/alerts`** — Alertmanager webhook receiver (when enabled). Accepts Alertmanager webhook payloads and routes them through the same dedupe/grouping/routing/sink pipeline. Optional bearer auth.
- **`GET /api/alerts`** — Read-only alerts API. JSON of active alerts plus recent history. Optional bearer auth.

Both endpoints serve on `metricsAddr` (default `:9090`) and return `503 Service Unavailable` until their handlers are installed (after the controller starts or the handler is explicitly registered).

## Enable the Alertmanager receiver

The receiver accepts Alertmanager webhook payloads without requiring any Kubernetes resource. Set `receiver.enabled: true` in your config and optionally configure bearer authentication.

### Step 1 — Enable in the config

```yaml
receiver:
  enabled: true
  allowAnonymous: false   # require a bearer token
```

!!! warning "Anonymous receiver requires explicit opt-in"
    By default, if `receiver.enabled: true` and no bearer token is set, the controller **refuses to start** — an open endpoint that accepts unauthenticated alerts is a security risk. Set `allowAnonymous: true` only if the metrics port is locked down by a NetworkPolicy or firewall rule.

### Step 2 — Set a bearer token (optional)

If you want to require bearer authentication, set the `ALERTKUBE_RECEIVER_TOKEN` environment variable (or the Helm `receiver.token` value):

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

=== "Docker (env var)"
    ```bash
    docker run -e ALERTKUBE_RECEIVER_TOKEN="your-secret-token-here" \
      ghcr.io/aryasoni98/alertkube:v0.2.4
    ```

When a token is set, the receiver requires an `Authorization: Bearer <token>` header on all requests. Without it, requests are rejected with `401 Unauthorized`.

!!! tip "Rotate the token without downtime"
    The receiver reads the token on every request, not at startup. Update the Secret and the new value takes effect on the next alert without restarting the controller.

### Step 3 — Point Alertmanager at the receiver

Configure your Alertmanager's `webhook_config` to send webhooks to alertkube:

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
      - url: 'http://alertkube.monitoring:9090/api/v1/alerts'
        send_resolved: true
        # If you set receiver.token:
        headers:
          Authorization: 'Bearer your-secret-token-here'
```

!!! note "Alerts routed through alertkube appear as `kind: External`"
    When an alert arrives via the receiver, the `kind` label is set to `External` to distinguish it from Kubernetes-native alerts. Use `kind: External` in routing rules if you want to treat Alertmanager-sourced alerts differently.

## Query the `/api/alerts` introspection endpoint

The `/api/alerts` endpoint serves a read-only snapshot of active alerts and recent history as JSON. It is always available (once the controller starts) regardless of the `receiver.enabled` setting.

### GET /api/alerts

```bash
# Without authentication
curl http://localhost:9090/api/alerts

# With bearer token authentication (if api.token is set)
curl -H "Authorization: Bearer your-secret-token-here" \
  http://localhost:9090/api/alerts
```

### Response format

The response is a JSON object with two keys:

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

- **`active`** — alerts currently firing (unresolved).
- **`recent`** — recently resolved or muted alerts (historical window for correlation).

### Protect the API endpoint

By default, `/api/alerts` is unauthenticated. Two ways to protect it:

=== "Bearer token"
    ```yaml
    # config.yaml
    # OR helm values:
    api:
      token: "your-api-token"
    ```
    
    Then require the `Authorization: Bearer <token>` header on all requests.

=== "NetworkPolicy"
    ```yaml
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: alertkube-api-restrict
    spec:
      podSelector:
        matchLabels:
          app.kubernetes.io/name: alertkube
      policyTypes:
        - Ingress
      ingress:
        - from:
            - podSelector:
                matchLabels:
                  role: monitoring  # only pods with this label can query
          ports:
            - protocol: TCP
              port: 9090
    ```

## Configuration reference

See the [configuration schema](../reference/config-schema.md) for the full details on `receiver` and `api` blocks.

| Path | Type | Default | Description |
| --- | --- | --- | --- |
| `receiver.enabled` | bool | `false` | Enable the Alertmanager webhook receiver on `POST /api/v1/alerts`. |
| `receiver.allowAnonymous` | bool | `false` | Allow requests without a bearer token (only safe if the port is NetworkPolicy-locked). |
| `api.token` | string | `""` | Optional bearer token for `/api/alerts` (empty = unauthenticated). |

Corresponding environment variables and Helm values:

| Env var | Helm value | Key | Notes |
| --- | --- | --- | --- |
| `ALERTKUBE_RECEIVER_TOKEN` | `receiver.token` / `receiver.tokenSecretKeyRef` | `receiverToken` | Bearer token for the receiver. Read on every request. |
| `ALERTKUBE_API_TOKEN` | `api.token` / `api.tokenSecretKeyRef` | `apiToken` | Bearer token for `/api/alerts`. Read on every request. |

## Example: inject alerts from another system

Here's a complete example that sets up the receiver with authentication and makes a test request:

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
curl -X POST http://localhost:9090/api/v1/alerts \
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

# 4. Query the API to see it recorded
curl -H "Authorization: Bearer my-api-token" \
  http://localhost:9090/api/alerts | jq '.active[] | select(.kind == "External")'
```

## See also

- [Configuration schema](../reference/config-schema.md) — full `receiver` and `api` block documentation.
- [Routing rules](../reference/config-schema.md#routing) — how to route receiver-sourced (`kind: External`) alerts to specific sinks.
- [Architecture](../architecture.md) — how the receiver fits into the alertkube pipeline.
