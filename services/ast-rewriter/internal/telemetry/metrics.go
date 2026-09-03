package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// InitMeter initialises an OTel MeterProvider backed by the Prometheus
// exposition-format bridge (pull-based — no collector or OTLP_ENDPOINT
// needed, unlike InitTracer) and returns a Meter to instrument with plus
// the http.Handler to mount at GET /metrics.
func InitMeter(serviceName string) (metric.Meter, http.Handler, error) {
	exporter, err := prometheus.New()
	if err != nil {
		return nil, nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(provider)
	return provider.Meter(serviceName), promhttp.Handler(), nil
}
