# Watcher conditions

Every alert reason emitted by each resource watcher, its default severity, and
the exact condition that triggers it. Default severities are hardcoded in the
watcher source and may be remapped with `severityOverrides`.

Reasons are the fourth component of the dedupe fingerprint
(`sha256(kind|namespace|name|reason)`) and are the values matched by
`routing`, `severityOverrides`, `inhibitions`, and `silences` `reason` keys
(which accept an anchored regex).

## Pod

`Kind: Pod`. Container waiting/terminated states are checked first and short-
circuit; per-restart alerts run only when no waiting/OOM state matched.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `CrashLoopBackOff` | critical | A container status has `state.waiting.reason == CrashLoopBackOff`. |
| `ImagePullBackOff` | warning | A container status has `state.waiting.reason == ImagePullBackOff`. |
| `ErrImagePull` | warning | A container status has `state.waiting.reason == ErrImagePull`. |
| `OOMKilled` | critical | A container's `lastTerminationState.terminated.reason == OOMKilled`. |
| `ContainerKilled` | warning | A container's last termination was a non-OOM SIGKILL (`exitCode == 137` or `signal == 9`) **and** the pod is not being deleted (`metadata.deletionTimestamp` unset). Catches liveness-probe escalation, `terminationGracePeriodSeconds` exceeded mid-run, and runtime force-kills. SIGKILL during normal teardown (rollout, scale-down, eviction) sets `deletionTimestamp`, so graceful shutdowns stay silent. |
| `ContainerRestart` | warning | Total restart count increased on update and is `<= behavior.ignoreRestartCount`; per container with `restartCount > 0`. Skipped if `ignoreRestartsWithExitCodeZero` and the last termination exit code was 0. |

All container alerts append the last-termination cause to the summary when present (e.g. `- last termination: SIGKILL (exit 137)` / `SIGTERM (exit 143)` / `Error (exit 1)`), so the signal/exit code is visible without opening the Container State block.

!!! note "Initial sync skips `ContainerRestart`"
    On the informer's initial sync (`AddFunc`), there is no previous pod to
    compute a restart delta, so only terminal/waiting conditions
    (`CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, `OOMKilled`,
    `ContainerKilled`) are evaluated. `ContainerRestart` fires only on an `UpdateFunc` where the count
    increased.

## Node

`Kind: Node`. Condition-type alerts fire only on a status *transition* (the
condition's status changed from the previous object).

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `NodeNotReady` | critical | `Ready` condition transitions to a status other than `True`. |
| `NodeMemoryPressure` | critical | `MemoryPressure` condition transitions to `True`. |
| `NodeDiskPressure` | critical | `DiskPressure` condition transitions to `True`. |
| `NodePIDPressure` | critical | `PIDPressure` condition transitions to `True`. |
| `NodeCordon` | warning | `spec.unschedulable` becomes `true` (was unset/false). |

!!! note "Pressure reasons are `Node` + the Kubernetes condition type"
    The pressure reasons are built as `"Node" + cond.Type`, yielding
    `NodeMemoryPressure`, `NodeDiskPressure`, and `NodePIDPressure`. Node alerts
    are disabled entirely when the chart is installed with `rbac.scope:
    namespace`, since nodes are cluster-scoped.

## Deployment

`Kind: Deployment`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `DeploymentUnavailable` | warning | `status.unavailableReplicas > 0`. |
| `ProgressDeadlineExceeded` | critical | A `Progressing` condition with `reason == ProgressDeadlineExceeded`. |

## StatefulSet

`Kind: StatefulSet`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `StatefulSetReplicasUnavailable` | warning | `spec.replicas` is set and non-zero, `status.readyReplicas < spec.replicas`, and `status.observedGeneration >= metadata.generation` (stale-spec guard). |

## DaemonSet

`Kind: DaemonSet`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `DaemonSetUnavailable` | warning | `status.numberUnavailable > 0`. |

## Job

`Kind: Job`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `JobFailed` | critical | A `Failed` condition with status `True` (backoffLimit hit). |

## CronJob

`Kind: CronJob`. Evaluated only on update events (requires a previous object).

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `CronJobSuspended` | info | `spec.suspend` transitions to `true` (was unset/false). |
| `CronJobMissingSuccess` | warning | A new `status.lastScheduleTime` arrived and the previous tick never produced a success (`lastSuccessfulTime` is nil or earlier than the old `lastScheduleTime`). |

!!! note "`CronJobMissingSuccess` does not parse cron expressions"
    Detection is event-driven: each new schedule tick is an Update event, and
    at that moment the watcher checks whether the *previous* tick ever
    succeeded. Individual failed runs already alert as `JobFailed` via the Job
    watcher.

## PersistentVolumeClaim

`Kind: PersistentVolumeClaim`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `PVCLost` | critical | `status.phase == Lost`. |
| `PVCPending` | warning | `status.phase == Pending` and the claim has existed longer than `behavior.pvcPendingSeconds`. |

!!! note "PVC pending threshold falls back to 5m if non-positive"
    The watcher uses `behavior.pvcPendingSeconds` seconds; if that value is
    `<= 0` it falls back to 5 minutes. (`Validate()` requires
    `pvcPendingSeconds > 0`, so this fallback only applies when validation is
    bypassed.)

## HorizontalPodAutoscaler

`Kind: HorizontalPodAutoscaler`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `HPAMaxedOut` | warning | `status.currentReplicas >= spec.maxReplicas` **and** a `ScalingLimited` condition with status `True` and `reason == TooManyReplicas`. |

!!! note "Sitting at max alone does not alert"
    Both conditions must hold: the HPA must be pinned at `maxReplicas` and the
    autoscaler must itself report `ScalingLimited == True` with reason
    `TooManyReplicas`. A workload that happens to need exactly `maxReplicas`
    does not alert.
