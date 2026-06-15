# End-to-end tests

These run alertkube against a real (kind) cluster and assert the full path:
**deploy → break a workload → the controller emits an alert**.

Two layers live here:

1. **Smoke (bash, in CI).** `.github/workflows/e2e.yml` installs the Helm chart
   with the `stdout` sink, creates a crash-looping pod, and asserts the alert
   appears in the controller logs. It runs across a Kubernetes version matrix and
   a separate HA (leader-election) job. This is the always-on baseline.

2. **Declarative ([chainsaw](https://kyverno.github.io/chainsaw/)).** Test specs
   under `chainsaw/` describe richer scenarios as data. Grow coverage here without
   writing bash. Run locally with:

   ```bash
   # Prereqs: kind + helm + chainsaw, alertkube image loaded into the cluster.
   kind create cluster
   docker build -t alertkube:e2e .
   kind load docker-image alertkube:e2e
   helm upgrade --install alertkube ./helm \
     --set image.repository=alertkube --set image.tag=e2e \
     --set image.pullPolicy=Never --set cluster=e2e \
     --set stdout.enabled=true
   chainsaw test test/e2e/chainsaw
   ```

## What is covered

| Scenario | Layer | Asserts |
| --- | --- | --- |
| Pod CrashLoopBackOff → alert | smoke + chainsaw | controller emits a `CrashLoopBackOff` alert for the pod |
| Chart installs & becomes Ready | smoke | Deployment rolls out, `/readyz` 200 |
| HA leader election | smoke (ha job) | 2 replicas run; exactly one is the leader |

## Adding a scenario

Prefer a chainsaw spec (`chainsaw/<name>/chainsaw-test.yaml`) — `apply` the
workload, then `assert`/`script` the expected alert. Keep each scenario isolated
in its own namespace so they can run concurrently.
