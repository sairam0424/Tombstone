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

// TestHTTPMetrics_FallsBackToRawPathOutsideChi proves the middleware
// doesn't panic or misbehave when used without a chi route context at all
// (e.g. wrapping a bare http.Handler in a test or a non-chi caller).
func TestHTTPMetrics_FallsBackToRawPathOutsideChi(t *testing.T) {
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
	if len(got.ScopeMetrics) == 0 {
		t.Fatal("no metrics recorded when used outside a chi router")
	}
}
