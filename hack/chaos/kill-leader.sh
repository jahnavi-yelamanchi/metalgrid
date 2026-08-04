#!/usr/bin/env bash
# Kill the operator leader pod and measure failover time to the standby.
set -euo pipefail
NS=metalgrid-system

lease_holder() {
  kubectl get lease -n "$NS" metalgrid-operator-leader -o jsonpath='{.spec.holderIdentity}' 2>/dev/null
}

holder=$(lease_holder)
echo "current leader identity: $holder"

# holderIdentity is "<pod-name>_<uuid>" for in-cluster leader election
# (controller-runtime uses the pod's own hostname == pod name).
leader_pod=$(echo "$holder" | cut -d_ -f1)

echo "killing leader pod: $leader_pod"
start=$(date +%s)
kubectl delete pod -n "$NS" "$leader_pod" --wait=false

echo "waiting for a new leader to acquire the lease..."
while [ "$(lease_holder)" = "$holder" ]; do
  sleep 1
done
end=$(date +%s)
echo "new leader acquired lease after $((end - start))s (was: $holder, now: $(lease_holder))"
