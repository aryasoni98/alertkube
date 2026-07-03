# alertkube Helm chart

Deploys [alertkube](https://github.com/aryasoni98/alertkube) - a Kubernetes
multi-resource alerting controller - with RBAC, metrics, optional HA, and
optional Prometheus Operator integration.

![Version: 1.2.0](https://img.shields.io/badge/Version-1.2.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 1.2.0](https://img.shields.io/badge/AppVersion-1.2.0-informational?style=flat-square)

## Install

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --version 1.2.0 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

Or from a git checkout: `helm upgrade --install alertkube ./helm --set cluster=...`.

### Sinks

Set the credential for each sink you use (inline or via `*SecretKeyRef`):

| Sink | Value(s) |
| --- | --- |
| Slack | `slack.webhookUrl` or `slack.botToken` (+ `slack.channels.*`) |
| PagerDuty | `pagerduty.routingKey` |
| Microsoft Teams | `teams.webhookUrl` |
| Opsgenie | `opsgenie.apiKey` (+ `opsgenie.apiUrl` for EU) |
| Discord | `discord.webhookUrl` |
| Telegram | `telegram.botToken` + `telegram.chatId` |
| Generic webhook | `genericWebhook.url` (+ `genericWebhook.signingSecret` for HMAC) |

Routing, inhibitions, silences, severity overrides, and escalations are set under
`routing:`, `inhibitions:`, `silences:`, `severityOverrides:`, `escalations:` and
rendered into the controller ConfigMap.

## Security

Runs as non-root (uid 65532), read-only root filesystem, all capabilities
dropped, `RuntimeDefault` seccomp. Credentials are sourced via Secrets
(`*SecretKeyRef`), never rendered as plaintext env values. See the repo
[`SECURITY.md`](https://github.com/aryasoni98/alertkube/blob/master/SECURITY.md).

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for scheduling. |
| api.allowSecretRead | bool | `false` | Opt-in (Phase 2b): allow the console to TEST a channel whose credential lives in a Kubernetes Secret. Enabling this grants the controller `secrets: get` in its OWN namespace (a Role, not cluster-wide) and is the one place the zero-secrets-read posture bends. Off by default; the secret value is read at send-time only and never returned to the client. Leave false unless you need in-UI channel credential validation. |
| api.allowUnauthenticatedRead | bool | `false` | Accept an UNAUTHENTICATED read API (/api/alerts + console data) when no api.token and no networkPolicy are set. The chart fails closed by default: an install with neither a token nor a NetworkPolicy is rejected unless this is explicitly true, so an open introspection surface is always a deliberate choice. Mirrors the receiver's allowAnonymous model. |
| api.authMode | string | `"token"` | Write-path auth mode: `token` (shared writeToken, default) or `rbac` (each write authenticated via Kubernetes TokenReview + SubjectAccessReview, so audit records a real username and access is managed with RBAC). `rbac` binds the controller SA to system:auth-delegator; grant END USERS access with a Role on apiGroups:["alertkube.io"] resources:["silences","channels"]. |
| api.token | string | `""` | Optional bearer token guarding read endpoints (`/api/alerts`, `/api/config`, `/api/silences` GET, console data) (inline; prefer the Secret ref). |
| api.tokenSecretKeyRef | object | `{}` | Secret reference for the API (read) token (`{key, name}`). |
| api.writeToken | string | `""` | Optional SEPARATE bearer token enabling runtime WRITES (create/delete silences from the console). Leave empty to keep the controller read-only: write endpoints fail closed (403) until this is set. Inline; prefer the ref. |
| api.writeTokenSecretKeyRef | object | `{}` | Secret reference for the API write token (`{key, name}`). |
| automountServiceAccountToken | bool | `true` | Mount the ServiceAccount API token into the pod. |
| aws.acm | bool | `false` | Alert on ACM certificates that are unusable or expiring within 30 days. |
| aws.asg | bool | `false` | Alert on Auto Scaling Groups below desired healthy capacity. |
| aws.aurora | bool | `false` | Alert on Aurora DB clusters in an unhealthy state. |
| aws.cloudtrail | bool | `false` | Alert on CloudTrail security changes (security-group / S3-policy / IAM). |
| aws.cloudtrailEvents | list | `[]` | Override CloudTrail event names to watch (empty = curated security set). |
| aws.cloudwatch | bool | `false` | Alert on CloudWatch alarms in ALARM state (covers EC2/ALB/NLB/RDS/custom metrics). |
| aws.dynamodb | bool | `false` | Alert on DynamoDB table status (inaccessible-encryption / archived). |
| aws.ebs | bool | `false` | Alert on EBS volumes whose status check is impaired. |
| aws.ec2 | bool | `false` | Alert on EC2 instance system/instance status-check failures. |
| aws.efs | bool | `false` | Alert on EFS file systems in the error lifecycle state. |
| aws.eks | bool | `false` | Alert on EKS cluster discovery + control-plane health. |
| aws.elasticache | bool | `false` | Alert on ElastiCache cluster status (incompatible-network / restore-failed). |
| aws.elbv2 | bool | `false` | Alert on ALB/NLB availability + target-group health (ELBv2). |
| aws.enabled | bool | `false` | Enable polling AWS APIs for cloud alerts (per-service toggles below:    EKS, CloudWatch, EC2, ELBv2, RDS, DynamoDB, ElastiCache, S3, CloudTrail,    ASG, KMS, EBS, Aurora, NAT, EFS, Route53, ACM, VPN). |
| aws.kms | bool | `false` | Alert on customer-managed KMS keys in a risky state (pending-deletion/disabled). |
| aws.nat | bool | `false` | Alert on NAT gateways in the failed state. |
| aws.pollSeconds | int | `60` | Seconds between polls (must be below behavior.resolveTTLSeconds). |
| aws.rds | bool | `false` | Alert on RDS DB instance status (failed/storage-full/stopped/...). |
| aws.regions | list | `[]` | AWS regions to poll. Required when aws.enabled is true. |
| aws.route53 | bool | `false` | Alert on Route53 health checks failing from a majority of checkers. |
| aws.s3 | bool | `false` | Alert on S3 buckets that are publicly accessible or not fully blocked. |
| aws.vpn | bool | `false` | Alert on Site-to-Site VPN connections with tunnels down. |
| azure.aks | bool | `false` | Alert on AKS cluster discovery + control-plane health. |
| azure.enabled | bool | `false` | Enable polling Azure APIs for cloud alerts (per-service toggles below:    AKS, Monitor, VMs, Storage, SQL, Redis). |
| azure.monitor | bool | `false` | Ingest fired Azure Monitor alerts (Alerts Management): Fired pages, Resolved resolves. |
| azure.pollSeconds | int | `60` | Seconds between polls (must be below behavior.resolveTTLSeconds). |
| azure.redis | bool | `false` | Alert on Azure Cache for Redis in a failed provisioning state. |
| azure.sql | bool | `false` | Alert on Azure SQL databases in an unhealthy status (Suspect/Offline/Inaccessible). |
| azure.storage | bool | `false` | Alert on Azure Storage account primary-endpoint unavailability. |
| azure.subscriptions | list | `[]` | Azure subscription IDs to poll. Required when azure.enabled is true. |
| azure.vms | bool | `false` | Alert on Azure VM provisioning failures. |
| behavior.disableAnnotationSilences | bool | `false` | Ignore `alert-silence-until` pod annotations (anti-self-silence). |
| behavior.disableLogCollection | bool | `false` | Disable previous-container log collection for alert enrichment. |
| behavior.ignoreRestartCount | int | `30` | Restart count above which CrashLoopBackOff stops re-alerting. |
| behavior.ignoreRestartsWithExitCodeZero | bool | `false` | Skip alerts for restarts that exited with code 0. |
| behavior.muteSeconds | int | `600` | Re-alert mute window in seconds for a still-firing condition. |
| behavior.pvcPendingSeconds | int | `300` | Seconds a PVC may stay Pending before alerting. |
| behavior.resolveTTLSeconds | int | `600` | Seconds an alert may stay unseen before it is treated as resolved. |
| behavior.startupGraceSeconds | int | `30` | Mute alerts fired during the first N seconds after start (0 disables). |
| client.burst | int | `100` | Client-side burst to the API server (0 = controller default of 100). |
| client.qps | int | `50` | Client-side QPS to the API server (0 = controller default of 50). |
| cluster | string | `"Change-Me"` | Cluster name shown in every alert. |
| crds.keep | bool | `true` | Keep CRDs on `helm uninstall` (helm.sh/resource-policy: keep) so active Silence objects survive a reinstall. Set false to let Helm delete them. |
| crds.silences.enabled | bool | `false` | Install the Silence CRD + RBAC and watch silences.alertkube.io. |
| discord.webhookUrl | string | `""` | Discord channel webhook URL (inline; prefer the Secret ref). |
| discord.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Discord webhook URL (`{key, name}`). |
| dispatch.queueSize | int | `0` | Delivery queue capacity (0 = controller default of 2048). |
| dispatch.workers | int | `0` | Delivery worker pool size (0 = controller default of 16). |
| escalations | list | `[]` | Escalation rules; re-dispatch unresolved alerts to extra sinks after a delay. |
| extraArgs | list | `[]` | Extra arguments appended to the controller command line. |
| extraEnv | list | `[]` | Extra environment variables for the controller container. Use this to inject static AWS credentials (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) when not using IRSA, or any other sink credential by env. |
| filters.ignoredNamespaces | string | `""` | Comma-separated namespaces to ignore. |
| filters.ignoredPodNamePrefixes | string | `""` | Comma-separated pod-name prefixes to ignore. |
| filters.watchedNamespaces | string | `""` | Comma-separated namespaces to watch (empty = all). |
| filters.watchedPodNamePrefixes | string | `""` | Comma-separated pod-name prefixes to watch. |
| fullnameOverride | string | `""` | Override the full resource name. |
| gcp.cloudsql | bool | `false` | Alert on Cloud SQL instance state (failed/suspended/maintenance). |
| gcp.compute | bool | `false` | Alert on Compute Engine instances in REPAIRING state. |
| gcp.enabled | bool | `false` | Enable polling Google Cloud APIs for cloud alerts (per-service toggles    below: GKE, Monitoring, Compute, CloudSQL). |
| gcp.gke | bool | `false` | Alert on GKE cluster discovery + health. |
| gcp.monitoring | bool | `false` | Cloud Monitoring posture: alert when an alert policy is disabled (not a fired-incident feed). |
| gcp.pollSeconds | int | `60` | Seconds between polls (must be below behavior.resolveTTLSeconds). |
| gcp.projects | list | `[]` | GCP project IDs to poll. Required when gcp.enabled is true. |
| genericWebhook.signingSecret | string | `""` | Optional HMAC-SHA256 signing key for request signatures. |
| genericWebhook.url | string | `""` | Endpoint that receives the raw Alert JSON via POST. |
| genericWebhook.urlSecretKeyRef | object | `{}` | Secret reference for the webhook URL (`{key, name}`). |
| googlechat.webhookUrl | string | `""` | Google Chat space incoming-webhook URL (inline; prefer the Secret ref). |
| googlechat.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Google Chat webhook URL (`{key, name}`). |
| grouping.by | list | `["kind","namespace","reason","severity"]` | Grouping key fields. |
| grouping.enabled | bool | `false` | Fold alert storms into one summary per group. |
| grouping.windowSeconds | int | `30` | Window in seconds during which same-group alerts collapse. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/aryasoni98/alertkube"` | Controller image repository. |
| image.tag | string | `""` | Image tag (defaults to the chart `appVersion`). |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| inhibitions | list | `[{"duration":"10m","equal":["node"],"source":{"kind":"Node","reason":"NodeNotReady"},"target":{"kind":"Pod"}}]` | Inhibition rules that suppress targets while a source alert is active. |
| leaderElection.enabled | bool | `false` | Enable HA leader election via a coordination Lease. |
| leaderElection.namespace | string | `""` | Namespace for the Lease (defaults to the release namespace). |
| maintenance | list | `[]` | Recurring daily maintenance windows that suppress matching alerts. |
| mattermost.webhookUrl | string | `""` | Mattermost incoming-webhook URL (inline; prefer the Secret ref). |
| mattermost.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Mattermost webhook URL (`{key, name}`). |
| metrics.enabled | bool | `true` | Expose the metrics/health HTTP server. |
| metrics.port | int | `9090` | Port for `/metrics`, `/healthz`, `/readyz`. |
| metrics.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator `ServiceMonitor`. |
| metrics.serviceMonitor.interval | string | `"30s"` | Scrape interval for the ServiceMonitor. |
| metrics.serviceMonitor.labels | object | `{}` | Extra labels on the ServiceMonitor. |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Per-scrape timeout (must be <= interval). |
| nameOverride | string | `""` | Override the chart name portion of resource names. |
| networkPolicy.apiServer.cidrs | list | `[]` | API server endpoint CIDRs (e.g. `["10.0.0.2/32"]`); required when enabled. |
| networkPolicy.apiServer.port | int | `443` | API server endpoint target port (often 6443 on self-managed). |
| networkPolicy.enabled | bool | `false` | Restrict metrics ingress and controller egress with a NetworkPolicy. |
| networkPolicy.extraEgress | list | `[]` | Extra egress rules appended verbatim. |
| networkPolicy.ingressFrom | list | `[]` | Sources allowed to scrape `/metrics` (empty = allow all). |
| networkPolicy.sinkCIDRs | list | `[]` | Sink endpoint CIDRs reachable on 443/TCP (empty = anywhere minus private ranges). |
| nodeSelector | object | `{}` | Node selector for scheduling. |
| opsgenie.apiKey | string | `""` | Opsgenie Alert API key (inline; prefer the Secret ref). |
| opsgenie.apiKeySecretKeyRef | object | `{}` | Secret reference for the Opsgenie API key (`{key, name}`). |
| opsgenie.apiUrl | string | `""` | API URL override for the EU region (`https://api.eu.opsgenie.com`). |
| pagerduty.routingKey | string | `""` | PagerDuty Events API v2 routing key (inline; prefer the Secret ref). |
| pagerduty.routingKeySecretKeyRef | object | `{}` | Secret reference for the routing key (`{key, name}`). |
| persistence.configMapName | string | `""` | State ConfigMap name (defaults to `<fullname>-state`). |
| persistence.enabled | bool | `true` | Snapshot active-alert and mute state to a ConfigMap. |
| podAnnotations | object | `{}` | Extra annotations on the controller pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget (only meaningful with leader election + replicaCount > 1; a single-replica controller should not set one). |
| podDisruptionBudget.maxUnavailable | int | `1` | Tolerated unavailable replicas during voluntary disruption. |
| podDisruptionBudget.minAvailable | string | `""` | Minimum available replicas (mutually exclusive with maxUnavailable). |
| podSecurityContext | object | `{"fsGroup":65532,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level security context. |
| pprof.enabled | bool | `false` | Enable /debug/pprof profiling (requires an api token; fail-closed). |
| prometheusRule.absentFor | string | `"10m"` | How long alertkube metrics may be absent before `AlertkubeAbsent` fires. |
| prometheusRule.additionalRules | list | `[]` | Extra PrometheusRule entries appended verbatim. |
| prometheusRule.dispatchInflightThreshold | int | `20` | `AlertkubeDispatchSaturated` in-flight threshold. |
| prometheusRule.enabled | bool | `false` | Create a self-health `PrometheusRule` (requires the Prometheus Operator). |
| prometheusRule.labels | object | `{}` | Extra labels so your Prometheus selects the rule. |
| rbac.scope | string | `"cluster"` | RBAC scope: `cluster` (all namespaces + nodes) or `namespace`. |
| receiver.allowAnonymous | bool | `false` | Run the receiver without a token. Required to enable the receiver with no token; otherwise startup fails closed (an open endpoint accepts unauthenticated alert injection). Only set true when the port is locked down by a NetworkPolicy. |
| receiver.enabled | bool | `false` | Accept Alertmanager webhooks on `/api/v1/alerts`. |
| receiver.token | string | `""` | Optional bearer token guarding the receiver (inline; prefer the Secret ref). |
| receiver.tokenSecretKeyRef | object | `{}` | Secret reference for the receiver token (`{key, name}`). |
| replicaCount | int | `1` | Replica count (>1 requires `leaderElection.enabled`). |
| resources.limits | object | `{"cpu":"200m","memory":"256Mi"}` | Resource limits. |
| resources.requests | object | `{"cpu":"50m","memory":"64Mi"}` | Resource requests. |
| routing | list | `[{"match":{"severity":"critical"},"sinks":["slack","pagerduty"]},{"match":{"severity":"warning"},"sinks":["slack"]},{"match":{"severity":"info"},"sinks":["slack"]}]` | Routing rules mapping alert matches to sinks (rendered into the ConfigMap). |
| rules | list | `[]` | Custom correlation rules; each fires a derived alert (kind Derived) when its condition holds. Exactly one of count/all/absent per rule. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":65532}` | Container-level security context. |
| serviceAccount.annotations | object | `{}` | Extra annotations on the controller ServiceAccount. For AWS IRSA set `eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<role>`. |
| severityOverrides | list | `[]` | Severity remap rules applied before dedupe/routing (first match wins). |
| silences | list | `[]` | Standing silence rules. |
| sinkRates | object | `{}` | Per-sink token-bucket rate overrides (default 1 msg/s, burst 5). |
| slack.botToken | string | `""` | Slack bot token (chat.postMessage); enables per-severity channels. |
| slack.botTokenSecretKeyRef | object | `{}` | Secret reference for the bot token (`{key, name}`). |
| slack.channels.critical | string | `"alerts-critical"` | Channel for critical alerts (bot-token mode only). |
| slack.channels.info | string | `"alerts-info"` | Channel for info alerts (bot-token mode only). |
| slack.channels.warning | string | `"alerts-warning"` | Channel for warning alerts (bot-token mode only). |
| slack.username | string | `"alertkube"` | Username shown on Slack webhook messages. |
| slack.webhookUrl | string | `""` | Slack incoming webhook URL (inline; prefer `webhookUrlSecretKeyRef`). |
| slack.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the webhook URL (`{key, name}`). |
| teams.webhookUrl | string | `""` | Microsoft Teams incoming webhook URL (inline; prefer the Secret ref). |
| teams.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Teams webhook URL (`{key, name}`). |
| telegram.botToken | string | `""` | Telegram bot token from @BotFather (inline; prefer the Secret ref). |
| telegram.botTokenSecretKeyRef | object | `{}` | Secret reference for the Telegram bot token (`{key, name}`). |
| telegram.chatId | string | `""` | Target chat/channel id (not secret). |
| terminationGracePeriodSeconds | int | `45` | Seconds the pod gets to drain before SIGKILL. |
| tolerations | list | `[]` | Tolerations for scheduling. |

## Full reference

The complete configuration reference is also on the docs site:
[Reference → Configuration](https://github.com/aryasoni98/alertkube/blob/master/docs-site/docs/reference/config-schema.md).

----------------------------------------------
_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml`, `values.yaml`, and `README.md.gotmpl`. Edit those, not `README.md`._
