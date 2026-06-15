#!/usr/bin/env bash
# Load harness: create N crash-looping pods to exercise dedup, storm folding,
# the enrichment pool, and sink rate limiting under pressure. Intended for a
# disposable cluster (kind/minikube), NOT production.
#
# Usage:   test/load/generate-pods.sh [COUNT] [NAMESPACE]
# Example: test/load/generate-pods.sh 1000 loadtest
# Cleanup: kubectl delete ns loadtest
set -euo pipefail

COUNT="${1:-500}"
NS="${2:-alertkube-loadtest}"

echo "Creating namespace ${NS} and ${COUNT} crash-looping pods..."
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

# A single Job/Deployment would share one fingerprint; we want distinct
# fingerprints (distinct pod names) to stress the dedup map and active-alert set.
for i in $(seq 1 "${COUNT}"); do
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: boom-${i}
  namespace: ${NS}
  labels: { app: alertkube-loadtest }
spec:
  restartPolicy: Always
  containers:
    - name: boom
      image: busybox:1.36
      command: ["/bin/false"]
      resources:
        requests: { cpu: "1m", memory: "8Mi" }
        limits:   { cpu: "10m", memory: "16Mi" }
EOF
  if (( i % 100 == 0 )); then echo "  created ${i}/${COUNT}"; fi
done

echo "Done. Watch alertkube under load:"
echo "  kubectl logs -f deploy/alertkube"
echo "  kubectl port-forward deploy/alertkube 9090:9090 &"
echo "  curl -s localhost:9090/metrics | grep -E 'alertkube_(active_alerts|alerts_total|dispatch_inflight|alerts_suppressed_total)'"
echo "Tear down with:  kubectl delete ns ${NS}"
