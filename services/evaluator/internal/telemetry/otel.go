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
	// never set — otelhttp.NewHandler (inbound) and otelhttp.NewTransport
	// (outbound, internal/httpclient.ResilientClient) both consult this
	// SAME global propagator to extract/inject W3C tracecontext headers.
	// Without this call, neither direction ever actually reads or writes a
	// traceparent header, so every inter-service HTTP hop starts a brand
	// new, disconnected trace regardless of whether a TracerProvider is
	// configured — this is why distributed tracing has never actually
	// worked end-to-end in this codebase. Set UNCONDITIONALLY, before the
	// OTLP_ENDPOINT check below: propagation must keep working even for a
	// service with local export disabled, so a trace chain passing THROUGH
	// it to a THIRD, tracing-enabled service isn't broken at this hop.
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
