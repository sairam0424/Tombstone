package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics builds a chi middleware recording RED (rate/errors/duration)
// metrics for every request. Labeled by chi's matched ROUTE PATTERN (e.g.
// "/api/v1/flags/{key}") and status-code CLASS ("2xx"/"4xx"/...), never the
// raw path or the exact status code — either would be an unbounded or
// needlessly wide label (a flag key embedded in the path; one series per
// exact code) that blows up cardinality for no analytical benefit.
func HTTPMetrics(meter metric.Meter) (func(http.Handler) http.Handler, error) {
	requestCount, err := meter.Int64Counter("http_server_requests_total",
		metric.WithDescription("Total HTTP requests received"))
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram("http_server_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"))
	if err != nil {
		return nil, err
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// Read AFTER ServeHTTP: chi populates the route pattern into the
			// request's RouteContext as it matches the route, so it's only
			// available once routing (which happens inside next.ServeHTTP)
			// has actually run.
			route := r.URL.Path
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}

			attrs := metric.WithAttributes(
				attribute.String("route", route),
				attribute.String("method", r.Method),
				attribute.String("status", strconv.Itoa(rec.status/100)+"xx"),
			)
			requestCount.Add(r.Context(), 1, attrs)
			requestDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		})
	}, nil
}

// statusRecorder captures the status code a handler actually writes —
// http.ResponseWriter has no getter of its own, so this is the standard way
// to observe it after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
