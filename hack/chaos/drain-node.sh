#!/usr/bin/env bash
# Drain a worker node while a job is running on it; confirm the job
# recovers (via retry-with-backoff) on another node instead of getting lost.
set -euo pipefail

NODE=${1:-metalgrid-worker}
JOB=chaos-drain-test

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: $JOB
spec:
  image: busybox:1.36
  command: ["sh", "-c", "sleep 60"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: platform
  pool: pool-worker
EOF

echo "waiting for $JOB to start running..."
until [ "$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ]; do sleep 2; done
node=$(kubectl get pod "$JOB" -o jsonpath='{.spec.nodeName}')
echo "$JOB is Running on node $node"

start=$(date +%s)
echo "draining $node..."
kubectl drain "$node" --ignore-daemonsets --delete-emptydir-data --force --timeout=60s || true

echo "waiting for $JOB to recover (retry) after losing its node..."
until [ "$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ]; do sleep 2; done
end=$(date +%s)
new_node=$(kubectl get pod "$JOB" -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "?")
echo "$JOB recovered and is Running again after $((end - start))s (was on $node, now on $new_node)"

echo "uncordoning $node..."
kubectl uncordon "$node"
kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
