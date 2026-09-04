package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInitMeter_HandlerServesPrometheusExpositionFormat proves InitMeter's
// returned handler is real and wired to the same MeterProvider it just set
// globally — a metric recorded through the returned Meter must show up when
// the returned handler is scraped, not just that neither call errors.
func TestInitMeter_HandlerServesPrometheusExpositionFormat(t *testing.T) {
	meter, handler, err := InitMeter("test-service")
	if err != nil {
		t.Fatalf("InitMeter: %v", err)
	}
	if meter == nil {
		t.Fatal("InitMeter returned a nil Meter")
	}
	if handler == nil {
		t.Fatal("InitMeter returned a nil handler")
	}

	counter, err := meter.Int64Counter("tombstone_test_init_meter_counter")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(t.Context(), 1)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tombstone_test_init_meter_counter") {
		t.Errorf("scrape response does not contain the metric recorded through the returned Meter — the handler and Meter are not actually wired to the same provider:\n%s", rec.Body.String())
	}
}
