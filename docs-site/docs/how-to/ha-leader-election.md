# Run alertkube in HA with leader election

Run more than one alertkube replica so a node loss or rollout never leaves the cluster unwatched, while guaranteeing only one replica ever dispatches alerts.

alertkube is a stateful controller: if two replicas both watched and dispatched, you would get duplicate pages and they could re-fire each other's resolves. Leader election solves this — exactly one replica (the leader) processes events and dispatches; the others stand by, healthy and ready to take over.

## Step 1 — scale up and enable leader election

```yaml
replicaCount: 2
leaderElection:
  enabled: true
  # Namespace holding the Lease object. Defaults to the release namespace
  # when empty; a shared "system" namespace lets you reinstall the release
  # without losing the lease.
  namespace: kube-system
```

Apply it:

```bash
helm upgrade alertkube ./helm --reuse-values \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set leaderElection.namespace=kube-system
```

!!! warning "`replicaCount > 1` requires leader election"
    The chart **refuses to render** when `replicaCount > 1` and `leaderElection.enabled=false` — two un-coordinated instances would double-dispatch and re-fire each other's alerts after a restart. Always turn both on together.

## Step 2 — understand follower behavior

Leadership is a `coordination.k8s.io/v1` Lease (15 s lease / 10 s renew / 2 s retry). The implications:

- **Only the leader dispatches.** Followers run the process but do not watch-and-dispatch; they wait to acquire the lease.
- **Followers stay healthy.** A follower serves `/metrics` and `/healthz` normally — standby is a healthy state, not a failure.
- **`/readyz` returns 503 on followers** until the replica acquires the lease. This is intentional: readiness reflects "am I the active controller," so dashboards and probes can tell leader from standby.

!!! note "Why followers are Ready at the pod level but 503 on /readyz"
    A subtle past bug had followers report not-ready, which deadlocked `RollingUpdate maxUnavailable: 0` rollouts (no pod could ever satisfy the probe). The current design keeps the *standby healthy* while `/readyz` still signals non-leadership — so rollouts proceed and you can still alert on "no leader."

## Step 3 — choose the deployment strategy

The chart picks the strategy for you based on the leader-election setting:

- **Leader election ON → `RollingUpdate`** with `maxSurge: 1`, `maxUnavailable: 0`. Leadership transfers to a healthy replica during the rollout, so there is no alerting gap.
- **Leader election OFF (`replicaCount: 1`) → `Recreate`.** The old pod is torn down before the new one starts, so two instances never overlap and re-fire each other's alerts.

`RollingUpdate maxUnavailable: 0` is the safe HA setting — see the [OPERATIONS guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md#ha-deployment).

## Step 4 — confirm lease RBAC

When `leaderElection.enabled=true`, the chart adds Lease RBAC in `leaderElection.namespace`:

- `coordination.k8s.io/leases`: `get, list, watch, create, update, patch, delete`
- `events`: `create, patch`

```bash
kubectl get role,rolebinding -n kube-system | grep alertkube
```

If you set `leaderElection.namespace` to a namespace the chart does not manage, ensure the ServiceAccount can reach the Lease there.

## Verify

1. Confirm two pods are running and the Lease exists with a holder:

    ```bash
    kubectl get pods -l app.kubernetes.io/name=alertkube
    kubectl get lease -n kube-system | grep alertkube
    ```

2. Confirm exactly one pod is the leader via its readiness:

    ```bash
    # leader -> 200, follower -> 503
    kubectl exec <leader-pod>   -- wget -qS -O- http://localhost:9090/readyz
    kubectl exec <follower-pod> -- wget -qS -O- http://localhost:9090/readyz
    ```

3. Delete the leader pod and confirm the follower acquires the lease within ~15 s, its `/readyz` flips to 200, and alert dispatch continues without duplicates.

!!! tip "Pair with state persistence"
    Persistence (on by default) snapshots active alerts and mute history to a ConfigMap, so a leadership handover does not lose pending resolves or re-page standing conditions. Keep `persistence.enabled: true` in HA. See the [OPERATIONS guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md#persistence).

## See also

- [Tune the mute window and storm folding](tune-mute-and-grouping.md) — controls dispatch volume regardless of replica count.
- [Suppress dependent alerts with inhibitions](configure-inhibition.md).
- [OPERATIONS guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) — SLOs, dashboards, and the full HA runbook.
