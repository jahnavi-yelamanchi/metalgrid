package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jahnavi-yelamanchi/metalgrid/internal/metrics"
)

// withMetrics records request duration under a caller-supplied route label.
// It must be applied per-registration (not as outer middleware wrapping the
// whole mux): net/http's ServeMux only fills in Request.Pattern on the
// request copy it hands to the matched handler, which a wrapper positioned
// before the mux never observes — reading it there would silently fall back
// to the raw URL and mint one timeseries per job ID.
func withMetrics(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		metrics.APIRequestDuration.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).
			Observe(time.Since(start).Seconds())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
