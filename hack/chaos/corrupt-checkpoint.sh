#!/usr/bin/env bash
# Corrupt a checkpoint job's persisted data between retries, confirming the
# checkpoint volume itself survives pod recreation (the system's job) while
# making clear that detecting/recovering from corrupted *content* is the
# job image's responsibility, not the control plane's.
set -euo pipefail
JOB=chaos-checkpoint-test

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
kubectl wait --for=delete pvc/"$JOB-checkpoint" --timeout=30s 2>/dev/null || true

cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: $JOB
spec:
  image: busybox:1.36
  command: ["sh", "-c", "echo good-data > \$METALGRID_CHECKPOINT_DIR/state && sleep 3"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: platform
  checkpoint: true
EOF

echo "waiting for $JOB to write its checkpoint and complete..."
until [ "$(kubectl get acceleratorjob "$JOB" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Succeeded" ]; do sleep 2; done
echo "checkpoint written; corrupting it via a throwaway pod mounting the same PVC..."

kubectl run corrupt-checkpoint --image=busybox:1.36 --restart=Never --overrides='
{
  "spec": {
    "containers": [{"name":"corrupt","image":"busybox:1.36","command":["sh","-c","echo CORRUPTED > /checkpoint/state"],
      "volumeMounts":[{"name":"cp","mountPath":"/checkpoint"}]}],
    "volumes": [{"name":"cp","persistentVolumeClaim":{"claimName":"'"$JOB"'-checkpoint"}}]
  }
}' >/dev/null
kubectl wait --for=condition=Ready pod/corrupt-checkpoint --timeout=30s >/dev/null 2>&1 || true
sleep 3
kubectl delete pod corrupt-checkpoint --ignore-not-found >/dev/null 2>&1

echo "confirming the checkpoint volume now holds the corrupted content (proves it persisted, unmanaged by us):"
kubectl run read-checkpoint --image=busybox:1.36 --restart=Never --overrides='
{
  "spec": {
    "containers": [{"name":"read","image":"busybox:1.36","command":["cat","/checkpoint/state"],
      "volumeMounts":[{"name":"cp","mountPath":"/checkpoint"}]}],
    "volumes": [{"name":"cp","persistentVolumeClaim":{"claimName":"'"$JOB"'-checkpoint"}}]
  }
}' >/dev/null
kubectl wait --for=condition=Ready pod/read-checkpoint --timeout=30s >/dev/null 2>&1 || true
sleep 2
kubectl logs read-checkpoint 2>&1
kubectl delete pod read-checkpoint --ignore-not-found >/dev/null 2>&1

kubectl delete acceleratorjob "$JOB" --ignore-not-found >/dev/null 2>&1
