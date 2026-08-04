#!/usr/bin/env bash
# Scale the mock device plugin's advertised device count to 0 mid-run,
# confirm new jobs stay cleanly Pending (not erroring) while capacity is
# gone, then restore it and confirm they schedule without resubmission.
set -euo pipefail
JOB=chaos-no-accel-test

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1

echo "scaling devplugin to 0 devices per node..."
kubectl set env daemonset/metalgrid-devplugin -n kube-system NUM_DEVICES=0
kubectl rollout status daemonset/metalgrid-devplugin -n kube-system --timeout=60s

total_capacity() {
  kubectl get nodes -o jsonpath='{range .items[*]}{.status.capacity.metalgrid\.dev/accelerator}{"\n"}{end}' \
    | python3 -c "import sys; print(sum(int(l) for l in sys.stdin if l.strip()))"
}

echo "waiting for advertised capacity to drop to 0..."
until [ "$(total_capacity)" = "0" ]; do
  sleep 2
done
echo "cluster-wide accelerator capacity is now 0"

cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: $JOB
spec:
  image: busybox:1.36
  command: ["sh", "-c", "echo ran && sleep 5"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: platform
EOF

sleep 5
phase=$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}')
echo "job phase with zero capacity available: $phase (expect Pending, not Failed)"

echo "restoring devplugin capacity..."
kubectl set env daemonset/metalgrid-devplugin -n kube-system NUM_DEVICES=4
kubectl rollout status daemonset/metalgrid-devplugin -n kube-system --timeout=60s

echo "waiting for $JOB to schedule now that capacity is back (no resubmission)..."
start=$(date +%s)
until [ "$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Succeeded" ]; do sleep 2; done
end=$(date +%s)
echo "$JOB completed $((end - start))s after capacity was restored, without ever being resubmitted"

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
