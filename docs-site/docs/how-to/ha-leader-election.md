# Run HA with Leader Election

Use leader election when running more than one replica. Only the leader dispatches alerts; followers stand by.

## Enable HA

```yaml
replicaCount: 2
leaderElection:
  enabled: true
  # Namespace holding the Lease object. Defaults to the release namespace
  # when empty; a shared "system" namespace lets you reinstall the release
  # without losing the lease.
  namespace: kube-system
```

Apply:

```bash
helm upgrade alertkube ./helm --reuse-values \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set leaderElection.namespace=kube-system
```

The chart refuses to render `replicaCount > 1` unless leader election is enabled.

## Follower Behavior

Leadership uses a `coordination.k8s.io/v1` Lease with a 30s lease / 20s renew / 5s retry profile (tuned for a workload pod renewing through the API server, unlike the tighter kube-controller-manager defaults):

- **Only the leader dispatches.** Followers run the process but do not watch-and-dispatch; they wait to acquire the lease.
- **Followers stay healthy.** A follower serves `/metrics` and `/healthz` normally - standby is a healthy state, not a failure.
- **`/readyz` returns 503 on followers** until the replica acquires the lease. This is intentional: readiness reflects "am I the active controller," so dashboards and probes can tell leader from standby.

## Deployment Strategy

- **Leader election ON → `RollingUpdate`** with `maxSurge: 1`, `maxUnavailable: 0`. Leadership transfers to a healthy replica during the rollout, so there is no alerting gap.
- **Leader election OFF (`replicaCount: 1`) → `Recreate`.** The old pod is torn down before the new one starts, so two instances never overlap and re-fire each other's alerts.

## Lease RBAC

The chart adds Lease RBAC in `leaderElection.namespace`:

- `coordination.k8s.io/leases`: `get, list, watch, create, update, patch, delete`
- `events`: `create, patch`

```bash
kubectl get role,rolebinding -n kube-system | grep alertkube
```

If the chart does not manage that namespace, ensure the ServiceAccount has Lease access.

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

Keep `persistence.enabled: true` in HA so handovers preserve pending resolves and mute history.

## Active/passive vs. active/active (sharding)

Leader election above is **active/passive**: replicas give you *failover*, not
more throughput — only the leader works. For very large clusters that need to
spread the watch/evaluate/dispatch load across replicas, alertkube also supports
**active/active sharding** (v1.2+).

Each replica watches everything but only *acts* on the objects it owns, where
ownership is a stable hash:

```
owns(object) == fnv32a("kind/namespace/name") mod SHARD_TOTAL == SHARD_INDEX
```

so at any instant exactly one replica owns a given object — no double-paging.
Enable it by giving each replica a distinct index out of a fixed total:

```
ALERTKUBE_SHARD_TOTAL=3     # number of shards (all replicas)
ALERTKUBE_SHARD_INDEX=0     # this replica's shard (0..TOTAL-1, unique per pod)
```

!!! important "Sharding needs a stable per-replica identity"
    Each replica must get a **unique, stable** `ALERTKUBE_SHARD_INDEX`. Run the
    shards as a **StatefulSet** (map the pod ordinal to the index via the
    Downward API / an init step) or as N separate Deployments, one per index.
    A plain Deployment cannot give replicas stable ordinals. Leave
    `ALERTKUBE_SHARD_TOTAL` unset/`1` (the default) for the standard
    single-active-replica model, which is unchanged.

Rebalancing is by rollout: change `ALERTKUBE_SHARD_TOTAL` and redeploy.

!!! note "Cross-shard correlation limitation"
    With sharding on, each replica's rule engine (`count`/`all` correlation
    rules) observes only *its shard's* alert stream, so a rule that counts
    across the whole cluster may under-count. Keep correlation rules on a single
    active replica (leader election, no sharding) if you rely on them.

The two models compose: use leader election for failover of a single active
replica, or sharding for horizontal scale (each shard can itself be a
leader-elected pair for failover).

## See Also

- [Tune the mute window and storm folding](tune-mute-and-grouping.md) - controls dispatch volume regardless of replica count.
- [Suppress dependent alerts with inhibitions](configure-inhibition.md).
- [OPERATIONS guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) - SLOs, dashboards, and the full HA runbook.
