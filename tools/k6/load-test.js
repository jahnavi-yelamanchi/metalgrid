// API load test for POST/GET /v1/jobs. k6 reports p50/p95/p99 on
// http_req_duration natively — no custom percentile code needed.
//
// Usage: k6 run tools/k6/load-test.js
//        BASE_URL=http://localhost:8080 VUS=20 DURATION=1m k6 run tools/k6/load-test.js
import http from "k6/http";
import { check, sleep } from "k6";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const VUS = parseInt(__ENV.VUS || "20", 10);
const DURATION = __ENV.DURATION || "1m";

export const options = {
  scenarios: {
    submit_and_poll: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "15s", target: VUS },
        { duration: DURATION, target: VUS },
        { duration: "15s", target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<1000"],
    http_req_failed: ["rate<0.01"],
  },
};

export default function () {
  const payload = JSON.stringify({
    team: "platform",
    image: "busybox:1.36",
    command: ["sh", "-c", "sleep 1"],
    acceleratorType: "mock-gpu",
    acceleratorCount: 1,
  });
  const params = { headers: { "Content-Type": "application/json" } };

  const createRes = http.post(`${BASE_URL}/v1/jobs`, payload, params);
  const created = check(createRes, { "create status 201": (r) => r.status === 201 });

  if (created) {
    const id = JSON.parse(createRes.body).id;
    const getRes = http.get(`${BASE_URL}/v1/jobs/${id}`);
    check(getRes, { "get status 200": (r) => r.status === 200 });
  }

  sleep(1);
}
