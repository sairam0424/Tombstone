"""Tests for app/observability/metrics.py — the Python counterpart to the
Go services' internal/telemetry.HTTPMetrics test suites. Each test builds
its own isolated CollectorRegistry (mirroring the Go tests' fresh
sdkmetric.NewMeterProvider per test) so tests never share process-wide
state with each other or with app.main's own production instruments.
"""

from __future__ import annotations

from prometheus_client import CollectorRegistry, generate_latest
from prometheus_client.parser import text_string_to_metric_families
from fastapi import FastAPI
from fastapi.testclient import TestClient
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import Response as StarletteResponse

from app.observability.metrics import (
    RedMetricsMiddleware,
    _new_instruments,
    build_red_metrics_dispatch,
    metrics_response,
)


def _samples(registry: CollectorRegistry, metric_name: str):
    text = generate_latest(registry).decode()
    for family in text_string_to_metric_families(text):
        if family.name != metric_name:
            continue
        for sample in family.samples:
            yield sample


def _build_app(registry: CollectorRegistry) -> FastAPI:
    count, duration = _new_instruments(registry)
    app = FastAPI()
    app.add_middleware(
        BaseHTTPMiddleware, dispatch=build_red_metrics_dispatch(count, duration)
    )
    return app


def test_records_route_method_and_status_class():
    """Direct proof the middleware records real data, labeled by the
    matched ROUTE TEMPLATE (not the raw request path) and a status-code
    CLASS (not the exact code)."""
    registry = CollectorRegistry()
    app = _build_app(registry)

    @app.get("/api/v1/flags/{key}")
    async def handler(key: str):
        return StarletteResponse(status_code=404)

    client = TestClient(app, raise_server_exceptions=False)
    client.get("/api/v1/flags/my-secret-flag-key-should-not-appear")

    samples = list(_samples(registry, "http_server_requests"))
    assert len(samples) == 1
    s = samples[0]
    assert s.name == "http_server_requests_total"
    assert s.value == 1
    assert s.labels["route"] == "/api/v1/flags/{key}"
    assert s.labels["method"] == "GET"
    assert s.labels["status"] == "4xx"


def test_records_duration():
    """The histogram actually receives a non-negative observation for
    every request, not just the counter."""
    registry = CollectorRegistry()
    app = _build_app(registry)

    @app.get("/health")
    async def health():
        return {"status": "ok"}

    client = TestClient(app)
    client.get("/health")

    counts = [
        s
        for s in _samples(registry, "http_server_request_duration_seconds")
        if s.name.endswith("_count")
    ]
    sums = [
        s
        for s in _samples(registry, "http_server_request_duration_seconds")
        if s.name.endswith("_sum")
    ]
    assert len(counts) == 1 and counts[0].value == 1
    assert len(sums) == 1 and sums[0].value >= 0


def test_falls_back_to_fixed_label_for_unmatched_route():
    """A genuinely unmatched route (a 404 inside a real app) must record
    the fixed "unmatched" label, not the raw, caller-controlled request
    path."""
    registry = CollectorRegistry()
    app = _build_app(registry)

    @app.get("/health")
    async def health():
        return {"status": "ok"}

    client = TestClient(app)
    r = client.get("/wp-admin/setup-config.php")
    assert r.status_code == 404

    samples = [
        s
        for s in _samples(registry, "http_server_requests")
        if s.name.endswith("_total")
    ]
    assert len(samples) == 1
    assert samples[0].labels["route"] == "unmatched"


def test_short_circuit_before_routing_uses_fixed_label():
    """A middleware registered BETWEEN this one and the router that
    returns without ever calling the downstream app must still fall back
    to "unmatched" — the route is never actually resolved."""
    registry = CollectorRegistry()
    count, duration = _new_instruments(registry)
    app = FastAPI()

    class ShortCircuit(BaseHTTPMiddleware):
        async def dispatch(self, request, call_next):
            return StarletteResponse(status_code=429)

    # Starlette wraps middleware in REVERSE registration order (the last
    # one added via add_middleware ends up outermost) — ShortCircuit must
    # be added FIRST so the metrics middleware, added second, wraps AROUND
    # it and is the one that actually sees this request, exactly mirroring
    # how app/main.py orders RedMetricsMiddleware relative to CORSMiddleware.
    app.add_middleware(ShortCircuit)
    app.add_middleware(
        BaseHTTPMiddleware, dispatch=build_red_metrics_dispatch(count, duration)
    )

    @app.get("/api/v1/flags/{key}")
    async def handler(key: str):
        return {"key": key}

    client = TestClient(app)
    r = client.get("/api/v1/flags/my-secret-flag-key-12345")
    assert r.status_code == 429

    samples = [
        s
        for s in _samples(registry, "http_server_requests")
        if s.name.endswith("_total")
    ]
    assert len(samples) == 1
    assert samples[0].labels["route"] == "unmatched"


def test_exception_recorded_as_5xx_and_still_propagates():
    """A handler that raises must still be recorded (as 5xx) and the
    exception must still propagate to Starlette's own error handling —
    mirrors the Go middleware's panic-then-record-then-repanic behavior."""
    registry = CollectorRegistry()
    app = _build_app(registry)

    @app.get("/boom")
    async def boom():
        raise ValueError("kaboom")

    client = TestClient(app, raise_server_exceptions=False)
    r = client.get("/boom")
    assert r.status_code == 500

    samples = [
        s
        for s in _samples(registry, "http_server_requests")
        if s.name.endswith("_total")
    ]
    assert len(samples) == 1
    assert samples[0].labels["route"] == "/boom"
    assert samples[0].labels["status"] == "5xx"


def test_route_template_not_raw_path_for_parameterized_route():
    """Direct regression proof: a real caller-supplied path segment (a
    flag key) must never appear in the recorded route label."""
    registry = CollectorRegistry()
    app = _build_app(registry)

    @app.get("/api/v1/anomaly/{flag_key}")
    async def anomaly(flag_key: str):
        return {"flag_key": flag_key}

    client = TestClient(app)
    client.get("/api/v1/anomaly/checkout-redesign-v2")

    samples = [
        s
        for s in _samples(registry, "http_server_requests")
        if s.name.endswith("_total")
    ]
    assert len(samples) == 1
    assert samples[0].labels["route"] == "/api/v1/anomaly/{flag_key}"
    assert "checkout-redesign-v2" not in generate_latest(registry).decode()


def test_production_middleware_and_endpoint_are_wired_to_the_same_registry():
    """Direct proof RedMetricsMiddleware and metrics_response() — the
    pieces actually wired into app/main.py — are connected to the SAME
    (default, process-wide) registry: a metric recorded through the real
    middleware must show up when the real /metrics handler is scraped, not
    just that neither call errors."""
    app = FastAPI()
    app.add_middleware(RedMetricsMiddleware)

    @app.get("/api/v1/production-wiring-probe")
    async def probe():
        return {"ok": True}

    @app.get("/metrics")
    async def metrics():
        return metrics_response()

    client = TestClient(app)
    client.get("/api/v1/production-wiring-probe")

    r = client.get("/metrics")
    assert r.status_code == 200
    assert 'route="/api/v1/production-wiring-probe"' in r.text, (
        "the real middleware and the real /metrics endpoint are not actually reading the same registry"
    )
