#!/usr/bin/env bash
# Exhaust a tenant's quota and confirm the admission webhook rejects the
# over-quota submission before it ever becomes a pod (not after scheduling).
set -euo pipefail
TEAM=chaos-quota-team

kubectl delete quotapolicy chaos-quota --ignore-not-found >/dev/null 2>&1
kubectl delete acceleratorjob -l metalgrid.dev/team="$TEAM" --ignore-not-found >/dev/null 2>&1

cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: QuotaPolicy
metadata:
  name: chaos-quota
spec:
  team: $TEAM
  maxAccelerators: 1
EOF

echo "submitting 1 job (within quota)..."
cat <<EOF | kubectl apply -f -
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: chaos-quota-job-1
  labels: { metalgrid.dev/team: "$TEAM" }
spec:
  image: busybox:1.36
  command: ["sh", "-c", "sleep 30"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: $TEAM
EOF

echo "submitting a 2nd job (over quota — expect rejection)..."
if cat <<EOF | kubectl apply -f - 2>&1; then
apiVersion: metalgrid.dev/v1alpha1
kind: AcceleratorJob
metadata:
  name: chaos-quota-job-2
  labels: { metalgrid.dev/team: "$TEAM" }
spec:
  image: busybox:1.36
  command: ["sh", "-c", "sleep 30"]
  acceleratorType: mock-gpu
  acceleratorCount: 1
  team: $TEAM
EOF
  echo "UNEXPECTED: over-quota job was admitted"
  exit 1
else
  echo "confirmed: over-quota submission rejected by the admission webhook"
fi

kubectl delete acceleratorjob chaos-quota-job-1 chaos-quota-job-2 --ignore-not-found >/dev/null 2>&1
kubectl delete quotapolicy chaos-quota --ignore-not-found >/dev/null 2>&1
