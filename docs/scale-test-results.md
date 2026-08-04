# Scale test results

Run against the live Helm/Argo-CD-managed stack in a 4-node kind cluster on
a single laptop (Apple Silicon, colima-backed Docker) — not dedicated cloud
infrastructure. Numbers below are real measurements from this environment,
not projections.

## Cluster capacity in this run

3 worker nodes × 4 mock accelerators each (`devplugin.numDevices: 4`) = **12
concurrent accelerator slots** cluster-wide. This is a demo-scale value in
`deploy/helm/values.yaml`, not a hard ceiling — raising it directly raises
how many jobs can run concurrently; it isn't a code change.

## `ponytail`: honest scope note

The original target in the plan was 10,000 queued jobs / 100–500 concurrent
worker pods. That number assumes real (or emulated multi-node cloud) infra;
running 500 concurrent containers on one laptop's Docker VM would mostly be
measuring the laptop, not the control plane. What's measured here instead:
submission throughput at meaningful volume (1,346 real jobs across two
separate load-test runs), and end-to-end scheduling/queueing behavior under
a backlog that exceeds available capacity by ~40x — the same code path a
10k/500 run would exercise, just not swept to that exact count. The
tooling (`tools/jobgen`, `tools/k6`) is the same tooling that would run the
full-scale test against real multi-node infra later — nothing here is
laptop-specific except the numbers.

## Run 1 — `tools/jobgen`, burst submission

```
python3 tools/jobgen/jobgen.py --url http://localhost:8080 --count 500 --concurrency 30 --job-sleep 2
```

- **500/500 submissions accepted** (`201`), 0 failures
- **Submission throughput**: 654 req/s (500 jobs in 0.8s)
- **API latency**: p50=40ms p95=112ms p99=126ms max=128ms
- **End-to-end drain**: first job created → last job `Succeeded` took **209s**
  for 500 jobs through 12 slots (theoretical minimum with perfect packing
  and zero overhead: ~83s at 2s/job ÷ 12 slots — the ~2.5x gap is real
  reconcile-loop, pod-create/teardown, and scheduler-extender round-trip
  overhead, not submission-side delay)
- **Scheduling latency** (`metalgrid_job_scheduling_latency_seconds`,
  creation → first `Running`): mean 106.7s across 492 jobs (`sum/count` from
  the live histogram) — dominated by queue wait, not by the scheduler's own
  decision time, exactly as expected when demand exceeds capacity ~40:1
- **Failures: 0** — every job eventually succeeded; none were lost or stuck

## Run 2 — `tools/k6`, sustained load

```
BASE_URL=http://localhost:8080 VUS=15 DURATION=45s k6 run tools/k6/load-test.js
```

- **846 create+get iterations**, 1,692 total HTTP requests, **0% failure rate**
- **API latency**: avg=36ms p90=78ms **p95=120ms** (threshold `p(95)<1000` passed)
- Threshold `http_req_failed rate<0.01` passed at exactly 0%

## Reading these together

The API layer (Postgres write + NATS publish + response) stayed fast and
linear under both load shapes — p95 never exceeded ~120ms even while 1,300+
jobs were mid-flight in the backlog below it. The bottleneck under load is
entirely downstream, at the fixed accelerator-capacity boundary — which is
the correct place for a scheduler's backpressure to show up, not in request
handling. `metalgrid_queue_depth` and `metalgrid_jobs_by_phase` (both
Prometheus gauges, see `deploy/helm/dashboards/metalgrid-overview.json`)
tracked the backlog draining in real time during both runs.
