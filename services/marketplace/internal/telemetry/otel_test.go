package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

// TestInitTracer_SetsGlobalTextMapPropagator is the direct regression proof
// for the outbound/inbound trace-propagation fix: the OTel Go SDK's global
// TextMapPropagator defaults to a no-op until something sets it — without
// this, neither otelhttp.NewHandler (inbound spans) nor
// internal/httpclient.ResilientClient's otelhttp.NewTransport (outbound
// spans) ever actually read or write a traceparent header, regardless of
// whether a TracerProvider is configured. Checked even when OTLP_ENDPOINT
// is unset: propagation must keep working for a service with local
// export disabled, so a trace chain passing THROUGH it to a third,
// tracing-enabled service isn't broken at this hop.
func TestInitTracer_SetsGlobalTextMapPropagator(t *testing.T) {
	t.Setenv("OTLP_ENDPOINT", "")

	shutdown, err := InitTracer(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer shutdown(context.Background()) //nolint:errcheck

	fields := otel.GetTextMapPropagator().Fields()
	if len(fields) == 0 {
		t.Fatal("the global propagator has zero fields — InitTracer left it as the no-op default, so no traceparent/baggage header will ever be injected or extracted")
	}

	wantFields := map[string]bool{"traceparent": false, "baggage": false}
	for _, f := range fields {
		if _, ok := wantFields[f]; ok {
			wantFields[f] = true
		}
	}
	for field, found := range wantFields {
		if !found {
			t.Errorf("global propagator does not carry the %q field — want both W3C TraceContext and Baggage wired", field)
		}
	}
}
