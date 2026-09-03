package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// InitTracer initialises the OpenTelemetry tracer provider.
// If OTLP_ENDPOINT is not set the function returns a no-op shutdown func so
// the service starts normally with zero overhead.
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	// The OTel Go SDK's global TextMapPropagator defaults to a no-op if
	// never set — otelhttp.NewHandler (inbound) reads this SAME global
	// propagator to extract a caller's W3C tracecontext header. ast-rewriter
	// makes no outbound calls of its own (no internal/httpclient package),
	// but its INBOUND spans still need this set to correctly continue a
	// caller's trace rather than always starting a disconnected root span.
	// Set UNCONDITIONALLY, before the OTLP_ENDPOINT check below:
	// propagation must keep working even for a service with local export
	// disabled.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	endpoint := os.Getenv("OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
