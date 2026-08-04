#!/usr/bin/env bash
# Submit the same Idempotency-Key twice and confirm exactly one job exists
# (not two), proving submission is idempotent under client retry.
set -euo pipefail
URL=${1:-http://localhost:8080}
KEY="chaos-dup-$(date +%s)"
BODY='{"team":"platform","image":"busybox:1.36","command":["sh","-c","sleep 5"],"acceleratorType":"mock-gpu","acceleratorCount":1}'

r1=$(curl -s -X POST "$URL/v1/jobs" -H "Idempotency-Key: $KEY" -H 'Content-Type: application/json' -d "$BODY")
r2=$(curl -s -X POST "$URL/v1/jobs" -H "Idempotency-Key: $KEY" -H 'Content-Type: application/json' -d "$BODY")

id1=$(echo "$r1" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")
id2=$(echo "$r2" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")

echo "request 1 -> $id1"
echo "request 2 -> $id2"

if [ "$id1" = "$id2" ]; then
  echo "confirmed: duplicate submission returned the same job, no second job created"
else
  echo "UNEXPECTED: duplicate submission created two different jobs"
  exit 1
fi

kubectl delete acceleratorjob "$id1" --ignore-not-found >/dev/null 2>&1
