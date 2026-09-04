package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestHTTPMetrics_RecordsRouteMethodAndStatusClass is the direct proof that
// the middleware records real data, labeled by chi's matched ROUTE PATTERN
// (not the raw request path, which could contain a flag key or other
// high-cardinality value) and a status-code CLASS (not the exact code).
func TestHTTPMetrics_RecordsRouteMethodAndStatusClass(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	r := chi.NewRouter()
	r.Use(mw)
	r.Get("/flags/{key}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/flags/my-secret-flag-key-should-not-appear", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var found bool
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_server_requests_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("http_server_requests_total is not an int64 Sum: %T", m.Data)
			}
			if len(sum.DataPoints) != 1 {
				t.Fatalf("got %d data points, want 1", len(sum.DataPoints))
			}
			dp := sum.DataPoints[0]
			if dp.Value != 1 {
				t.Errorf("count = %d, want 1", dp.Value)
			}
			route, ok := dp.Attributes.Value("route")
			if !ok || route.AsString() != "/flags/{key}" {
				t.Errorf("route attribute = %v, want the chi PATTERN /flags/{key}, not the raw path — a real flag key must never appear in a metric label", route)
			}
			method, ok := dp.Attributes.Value("method")
			if !ok || method.AsString() != http.MethodGet {
				t.Errorf("method attribute = %v, want GET", method)
			}
			status, ok := dp.Attributes.Value("status")
			if !ok || status.AsString() != "4xx" {
				t.Errorf("status attribute = %v, want 4xx (not the exact code 404)", status)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("http_server_requests_total metric was never recorded")
	}
}

// TestHTTPMetrics_RecordsDuration proves the histogram actually receives a
// non-negative observation for every request, not just the counter.
func TestHTTPMetrics_RecordsDuration(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	r := chi.NewRouter()
	r.Use(mw)
	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var found bool
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_server_request_duration_seconds" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("http_server_request_duration_seconds is not a float64 Histogram: %T", m.Data)
			}
			if len(hist.DataPoints) != 1 {
				t.Fatalf("got %d data points, want 1", len(hist.DataPoints))
			}
			if hist.DataPoints[0].Count != 1 {
				t.Errorf("observation count = %d, want 1", hist.DataPoints[0].Count)
			}
			if hist.DataPoints[0].Sum < 0 {
				t.Errorf("duration sum = %f, want >= 0", hist.DataPoints[0].Sum)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("http_server_request_duration_seconds metric was never recorded")
	}
}

// routeAttr returns the "route" attribute value recorded against
// http_server_requests_total, or "" if the metric was never recorded.
func routeAttr(t *testing.T, rm *metricdata.ResourceMetrics) string {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_server_requests_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || len(sum.DataPoints) == 0 {
				continue
			}
			if v, ok := sum.DataPoints[0].Attributes.Value("route"); ok {
				return v.AsString()
			}
		}
	}
	return ""
}

// TestHTTPMetrics_FallsBackToFixedLabelOutsideChi proves the middleware
// doesn't panic when used without a chi route context at all (e.g.
// wrapping a bare http.Handler in a test or a non-chi caller), and that
// the fallback is the FIXED "unmatched" label, never the raw request path.
func TestHTTPMetrics_FallsBackToFixedLabelOutsideChi(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/no-chi-here", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if route := routeAttr(t, &got); route != "unmatched" {
		t.Errorf("route = %q, want the fixed label \"unmatched\"", route)
	}
}

// TestHTTPMetrics_UnmatchedChiRouteUsesFixedLabelNotRawPath is the direct
// regression proof for the confirmed high-severity finding: a genuinely
// unmatched route (a 404 inside a real chi router) must record the fixed
// "unmatched" label, not the raw, caller-controlled request path — that
// path is exactly what an unauthenticated internet scanner controls, and
// the OTel SDK's shared, non-evicting cardinality budget (2000 by default)
// would otherwise be permanently exhaustible by routine probing, silently
// collapsing ALL future labels (including legitimate ones) into one opaque
// overflow bucket for the rest of the process's life.
func TestHTTPMetrics_UnmatchedChiRouteUsesFixedLabelNotRawPath(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	r := chi.NewRouter()
	r.Use(mw)
	r.Get("/flags/{key}", func(w http.ResponseWriter, req *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/wp-admin/setup-config.php", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (sanity check that the route genuinely didn't match)", rec.Code)
	}

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if route := routeAttr(t, &got); route != "unmatched" {
		t.Errorf("route = %q, want the fixed label \"unmatched\" — recording the raw path leaks attacker-controlled cardinality and, for a matched-but-different endpoint, could leak a real flag key", route)
	}
}

// TestHTTPMetrics_ShortCircuitBeforeChiRoutingUsesFixedLabel simulates the
// confirmed CORS-preflight/rate-limit/load-shed failure mode: a middleware
// registered BETWEEN this one and chi's tree router that returns without
// calling next — so the tree router, and therefore route-pattern
// population, never runs at all, even though the real path (which could
// embed a genuine flag key) is fully formed on the request.
func TestHTTPMetrics_ShortCircuitBeforeChiRoutingUsesFixedLabel(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	shortCircuit := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests) // never calls next — mirrors ratelimit.go/loadshed.go
		})
	}

	r := chi.NewRouter()
	r.Use(mw)
	r.Use(shortCircuit)
	r.Get("/flags/{key}", func(w http.ResponseWriter, req *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/flags/my-secret-flag-key-12345", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (sanity check that the short-circuit actually ran)", rec.Code)
	}

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if route := routeAttr(t, &got); route != "unmatched" {
		t.Errorf("route = %q, want the fixed label \"unmatched\" — the real request path (containing a real flag key here) must never leak into a metric label", route)
	}
}

// TestHTTPMetrics_PanicRecordedAndRePanicked is the direct regression proof
// for the confirmed high-severity finding: a panicking handler must still
// be recorded (as a 5xx) and the panic must still propagate so an outer
// recoverer (chi's Recoverer, registered outside this middleware in
// cmd/main.go) still produces the actual error response.
func TestHTTPMetrics_PanicRecordedAndRePanicked(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	mw, err := HTTPMetrics(meter)
	if err != nil {
		t.Fatalf("HTTPMetrics: %v", err)
	}

	r := chi.NewRouter()
	r.Use(mw)
	r.Get("/flags/{key}", func(w http.ResponseWriter, req *http.Request) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/flags/some-key", nil)
	rec := httptest.NewRecorder()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		r.ServeHTTP(rec, req)
	}()

	if recovered == nil {
		t.Fatal("the panic did not propagate out of HTTPMetrics — an outer recoverer would never see it")
	}

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if route := routeAttr(t, &got); route != "/flags/{key}" {
		t.Errorf("route = %q, want /flags/{key} — the route pattern was already resolved before the panic, so it should still be captured", route)
	}
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_server_requests_total" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			if len(sum.DataPoints) != 1 {
				t.Fatalf("got %d data points, want 1", len(sum.DataPoints))
			}
			status, _ := sum.DataPoints[0].Attributes.Value("status")
			if status.AsString() != "5xx" {
				t.Errorf("status = %q, want 5xx for a panicking request", status.AsString())
			}
		}
	}
}
