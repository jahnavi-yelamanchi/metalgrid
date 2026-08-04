// Package metrics defines MetalGrid's custom Prometheus metrics. apiserver
// registers the API-side metrics into the default registry (served via
// promhttp on /metrics); operator registers the controller-side metrics into
// controller-runtime's own registry (served by the manager's existing
// metrics server) so there's exactly one /metrics endpoint per process.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	JobsCreatedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "metalgrid_jobs_created_total",
		Help: "Total AcceleratorJob submissions accepted, by team.",
	}, []string{"team"})

	APIRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "metalgrid_api_request_duration_seconds",
		Help:    "REST API request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status"})

	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "metalgrid_queue_depth",
		Help: "Pending (unconsumed) job submissions in the NATS JetStream queue.",
	})

	// ponytail: a point-in-time gauge, not a completions counter — it answers
	// "how many jobs are in each phase right now," not "how many completed
	// over the last 24h" (a terminal job's phase count doesn't decay, but a
	// deleted job's does, so a rate() over this is not a true completion
	// rate). Upgrade path: add a metalgrid_jobs_completed_total counter
	// (labels: phase) incremented once per terminal transition if a
	// rate-based SLI is needed.
	JobPhaseCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "metalgrid_jobs_by_phase",
		Help: "Current AcceleratorJob count by phase.",
	}, []string{"phase"})

	SchedulingLatencySeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "metalgrid_job_scheduling_latency_seconds",
		Help:    "Time from AcceleratorJob creation to its first pod reaching Running.",
		Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 120},
	})

	AcceleratorUtilization = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "metalgrid_accelerator_utilization_ratio",
		Help: "Fraction of cluster-wide accelerator capacity currently allocated (0-1).",
	})
)

// RegisterAPI registers the metrics the API server updates.
func RegisterAPI(reg prometheus.Registerer) {
	reg.MustRegister(JobsCreatedTotal, APIRequestDuration)
}

// RegisterOperator registers the metrics the operator updates.
func RegisterOperator(reg prometheus.Registerer) {
	reg.MustRegister(QueueDepth, JobPhaseCount, SchedulingLatencySeconds, AcceleratorUtilization)
}
