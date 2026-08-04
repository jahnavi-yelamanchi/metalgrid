#!/usr/bin/env bash
# Delete a job's Running pod directly (not a container-level failure); the
# reconciler should notice it's missing and recreate a fresh one via the
# normal Owns(&corev1.Pod{}) watch — this is a different path than the
# retry-after-failure backoff logic (no failure was ever observed here).
set -euo pipefail
JOB=chaos-delete-pod-test

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: $JOB
spec:
  image: busybox:1.36
  command: ["sh", "-c", "sleep 30"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: platform
EOF

echo "waiting for $JOB to start running..."
until [ "$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ]; do sleep 2; done
uid_before=$(kubectl get pod "$JOB" -o jsonpath='{.metadata.uid}')
echo "$JOB pod uid before: $uid_before"

start=$(date +%s)
kubectl delete pod "$JOB" --wait=false
echo "deleted pod; waiting for the reconciler to recreate it..."

while true; do
  uid_now=$(kubectl get pod "$JOB" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
  if [ -n "$uid_now" ] && [ "$uid_now" != "$uid_before" ]; then
    break
  fi
  sleep 1
done
end=$(date +%s)
echo "pod recreated (new uid: $uid_now) after $((end - start))s"

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
