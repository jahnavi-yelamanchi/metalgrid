# MetalGrid SLIs/SLOs

## API availability

- **SLI**: proportion of `/v1/*` requests that don't return 5xx.
- **SLO**: 99.5% over a rolling 30 days.
- **Query**: `sum(rate(metalgrid_api_request_duration_seconds_count{status!~"5.."}[30d])) / sum(rate(metalgrid_api_request_duration_seconds_count[30d]))`

## API latency

- **SLI**: p99 request duration for `POST /v1/jobs` and `GET /v1/jobs/{id}`.
- **SLO**: p99 < 500ms over a rolling 5 minutes.
- **Query**: `histogram_quantile(0.99, sum by (le) (rate(metalgrid_api_request_duration_seconds_bucket{path=~"POST /v1/jobs|GET /v1/jobs/.*"}[5m])))`

## Job scheduling success rate

- **SLI**: proportion of AcceleratorJobs that reach `Succeeded` without exhausting retries.
- **SLO**: 99% over a rolling 24 hours.
- **Query**: `sum(metalgrid_jobs_by_phase{phase="Succeeded"}) / sum(metalgrid_jobs_by_phase)` — a point-in-time gauge ratio, not a true rolling-window rate; see the `ponytail:` note on `JobPhaseCount` in `internal/metrics/metrics.go` for why, and the counter it'd take to do this properly.

## Job scheduling latency

- **SLI**: p95 time from job creation to its pod reaching `Running`.
- **SLO**: p95 < 30s under normal cluster load (uncontended capacity).
- **Query**: `histogram_quantile(0.95, sum by (le) (rate(metalgrid_job_scheduling_latency_seconds_bucket[15m])))`

---

Matching Prometheus recording rules live in
[`deploy/helm/prometheus-rules.yaml`](../deploy/helm/prometheus-rules.yaml).
