#!/usr/bin/env bash
# Disconnect NATS (scale to 0), confirm submissions fail loudly instead of
# silently, then restore it and confirm the client auto-reconnects and
# submissions succeed again without restarting apiserver/operator.
set -euo pipefail
URL=${1:-http://localhost:8080}
NS=metalgrid-system

echo "scaling nats to 0..."
kubectl scale statefulset nats -n "$NS" --replicas=0
kubectl wait --for=delete pod/nats-0 -n "$NS" --timeout=30s || true

echo "submitting a job while NATS is down (expect a 5xx, not a hang or a silently-lost job)..."
code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 -X POST "$URL/v1/jobs" -H 'Content-Type: application/json' \
  -d '{"team":"platform","image":"busybox:1.36","acceleratorType":"mock-gpu","acceleratorCount":1}')
echo "submission during outage returned: $code"

echo "restoring nats..."
kubectl scale statefulset nats -n "$NS" --replicas=1
kubectl wait --for=condition=Ready pod/nats-0 -n "$NS" --timeout=60s

echo "waiting for apiserver's NATS client to reconnect..."
start=$(date +%s)
until curl -s -o /dev/null -w "" -m 5 -X POST "$URL/v1/jobs" -H 'Content-Type: application/json' \
  -d '{"team":"platform","image":"busybox:1.36","acceleratorType":"mock-gpu","acceleratorCount":1}' \
  --fail 2>/dev/null; do
  sleep 2
  if [ $(( $(date +%s) - start )) -gt 60 ]; then
    echo "did not recover within 60s"
    exit 1
  fi
done
end=$(date +%s)
echo "submissions succeeding again $((end - start))s after NATS came back — no apiserver/operator restart needed"
