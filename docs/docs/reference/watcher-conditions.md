# Watcher conditions

Every alert reason emitted by each resource watcher, its default severity, and
the exact condition that triggers it. Default severities are hardcoded in the
watcher source and may be remapped with `severityOverrides`.

Reasons are the fourth component of the dedupe fingerprint (sha256 over the
length-prefixed kind, namespace, name, and reason) and are the values matched by
`routing`, `severityOverrides`, `inhibitions`, and `silences` `reason` keys
(which accept an anchored regex).

## Pod

`Kind: Pod`. Every entry in `status.containerStatuses` is checked (init and
ephemeral containers are not) and the highest-severity finding is emitted; the
first finding wins a tie, so an OOM crash loop alerts as `CrashLoopBackOff`
with the OOM kill in its summary. A plain `ContainerRestart` never displaces a
waiting or kill finding.

The termination checked for `OOMKilled` and `ContainerKilled` is the
container's current `state.terminated`, or its `lastTerminationState.terminated`
while it is not running. A running container's `lastTerminationState` counts
only on the update where its `restartCount` increased. Neither reason is
limited by `behavior.ignoreRestartCount`. Under `restartPolicy: Never` a
current termination stays in place, so its alert is re-asserted on every
resync until the pod is deleted.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `CrashLoopBackOff` | critical | A container status has `state.waiting.reason == CrashLoopBackOff`. |
| `ImagePullBackOff` | warning | A container status has `state.waiting.reason == ImagePullBackOff`. |
| `ErrImagePull` | warning | A container status has `state.waiting.reason == ErrImagePull`. |
| `OOMKilled` | critical | The checked termination has `reason == OOMKilled`. |
| `ContainerKilled` | warning | The checked termination was a non-OOM SIGKILL (`exitCode == 137` or `signal == 9`) **and** the pod is not being deleted (`metadata.deletionTimestamp` unset). Catches liveness-probe escalation, `terminationGracePeriodSeconds` exceeded mid-run, and runtime force-kills. SIGKILL during API-initiated teardown (rollout, scale-down, drain eviction) sets `deletionTimestamp`, so graceful shutdowns stay silent. Kubelet-initiated teardown (node-pressure eviction, graceful node shutdown, `activeDeadlineSeconds`) does not set it; a current termination is skipped when the pod is `Failed` with a pod-level `status.reason` (e.g. `Evicted`) or the container reason is `ContainerStatusUnknown`. |
| `ContainerRestart` | warning | On update, a container's `restartCount` is higher than the same container's count in the previous object (each container is compared on its own, not a pod total), the restart was not an OOM kill or SIGKILL, and the container's new count is `<= behavior.ignoreRestartCount`. Skipped if `ignoreRestartsWithExitCodeZero` and the last termination exit code was 0. |

All container alerts append the cause of the termination they were classified from when present (e.g. `- last termination: OOMKilled (exit 137)` / `SIGKILL (exit 137)` / `SIGTERM (exit 143)` / `Error (exit 1)`), so the reason or signal is visible without opening the Container State block. An informative reason such as `OOMKilled` is shown instead of the signal it arrived with.

!!! note "Initial sync skips `ContainerRestart`"
    On the informer's initial sync (`AddFunc`), there is no previous pod to
    compute a restart delta, so only terminal/waiting conditions
    (`CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, `OOMKilled`,
    `ContainerKilled`) are evaluated. `ContainerRestart` fires only on an `UpdateFunc` where the count
    increased. A running container's past OOM kill or SIGKILL does not fire on
    initial sync either.

## Node

`Kind: Node`. On an ordinary update a condition alert fires only on a status
*transition* (the condition's status changed from the previous object). The
initial sync (`AddFunc`) has no previous object, so every condition counts as a
transition. A periodic informer resync re-asserts the current state, so a
condition or cordon that persists keeps its alert firing instead of resolving
after `behavior.resolveTTLSeconds`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `NodeNotReady` | critical | `Ready` condition has a status other than `True` (on a transition or resync). |
| `NodeMemoryPressure` | critical | `MemoryPressure` condition is `True` (on a transition or resync). |
| `NodeDiskPressure` | critical | `DiskPressure` condition is `True` (on a transition or resync). |
| `NodePIDPressure` | critical | `PIDPressure` condition is `True` (on a transition or resync). |
| `NodeCordon` | warning | `spec.unschedulable` becomes `true` (was unset/false, or on initial sync), and re-asserted on every resync while it stays `true`. |

!!! note "Pressure reasons are `Node` + the Kubernetes condition type"
    The pressure reasons are built as `"Node" + cond.Type`, yielding
    `NodeMemoryPressure`, `NodeDiskPressure`, and `NodePIDPressure`. Node alerts
    are disabled entirely when the chart is installed with `rbac.scope:
    namespace`, since nodes are cluster-scoped.

## Deployment

`Kind: Deployment`. Neither reason is evaluated while
`status.observedGeneration < metadata.generation`: that status was written
before the controller observed the current spec. The controller advances
`observedGeneration` in the same status write that reports the new replica
counts, so a scale-up or rolling update that leaves replicas unavailable still
fires `DeploymentUnavailable`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `DeploymentUnavailable` | warning | `status.unavailableReplicas > 0` and `status.observedGeneration >= metadata.generation`. |
| `ProgressDeadlineExceeded` | critical | A `Progressing` condition with `status == False` and `reason == ProgressDeadlineExceeded`, and `status.observedGeneration >= metadata.generation`. |

## StatefulSet

`Kind: StatefulSet`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `StatefulSetReplicasUnavailable` | warning | `spec.replicas` is set and non-zero, `status.readyReplicas < spec.replicas`, and `status.observedGeneration >= metadata.generation` (stale-spec guard). |

## DaemonSet

`Kind: DaemonSet`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `DaemonSetUnavailable` | warning | `status.numberUnavailable > 0` and `status.observedGeneration >= metadata.generation` (skips status written before the controller observed the current spec; a rollout's shortfall still fires). |

## Job

`Kind: Job`.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `JobFailed` | critical | A `Failed` condition with status `True` (backoffLimit hit). |

## CronJob

`Kind: CronJob`. Evaluated only on update events, which include the informer's
periodic resync (requires a previous object); the initial sync (`AddFunc`) is
not evaluated.

| Reason | Default severity | Trigger |
| --- | --- | --- |
| `CronJobSuspended` | info | `spec.suspend` transitions to `true` (was unset/false). Not re-asserted on resync, so a job suspended before startup stays silent and the alert resolves after `behavior.resolveTTLSeconds`. |
| `CronJobMissingSuccess` | warning | A new `status.lastScheduleTime` arrived and the previous tick never produced a success (`lastSuccessfulTime` is nil or earlier than the old `lastScheduleTime`). A resync re-asserts it while the job is not suspended, `status.active` is empty, and `lastSuccessfulTime` is nil or earlier than the current `lastScheduleTime`. So with no active run it fires once the latest run ends without success, before the next tick. While a run is active a resync skips it, so an alert can resolve after `behavior.resolveTTLSeconds` during a long run. |

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
