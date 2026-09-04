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
// "/api/v1/stream") and status-code CLASS ("2xx"/"4xx"/...), never the raw
// path or the exact status code — either would be an unbounded or needlessly
// wide label that blows up cardinality for no analytical benefit.
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

			// A defer, not plain post-call statements: a panic in next
			// unwinds straight through this frame, skipping any code after
			// next.ServeHTTP below — only deferred functions run during
			// that unwind. Without this, the exact requests RED metrics
			// most need to surface (application panics, caught and turned
			// into a 500 by chi's Recoverer further up the stack) would be
			// silently invisible: not mis-recorded, simply never counted.
			defer func() {
				p := recover()

				// Read AFTER ServeHTTP returns or panics: chi populates the
				// route pattern into the request's RouteContext as it
				// matches the route, deep inside next.ServeHTTP's call
				// chain. If ANY middleware between this one and chi's tree
				// router short-circuits without calling its own next, the
				// tree router never runs at all and no pattern is ever
				// populated — and a genuinely unmatched path (a 404,
				// including anything an unauthenticated caller cares to
				// probe) never populates one either. In every one of those
				// cases this falls back to a single FIXED label,
				// "unmatched", never the raw r.URL.Path — the path could
				// carry attacker-controlled cardinality (an unauthenticated
				// caller minting unlimited distinct path values, each
				// consuming one slot of the OTel SDK's shared, non-evicting
				// cardinality budget, 2000 by default, until it fills —
				// permanently collapsing ALL future new labels, including
				// legitimate ones added by a later deploy, into one opaque
				// overflow series for the rest of the process's life).
				route := "unmatched"
				if rctx := chi.RouteContext(r.Context()); rctx != nil {
					if pattern := rctx.RoutePattern(); pattern != "" {
						route = pattern
					}
				}

				status := rec.status
				if p != nil {
					status = http.StatusInternalServerError
				}

				attrs := metric.WithAttributes(
					attribute.String("route", route),
					attribute.String("method", r.Method),
					attribute.String("status", strconv.Itoa(status/100)+"xx"),
				)
				requestCount.Add(r.Context(), 1, attrs)
				requestDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)

				if p != nil {
					panic(p) // re-panic so Recoverer (registered outside this middleware) still handles it
				}
			}()

			next.ServeHTTP(rec, r)
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
