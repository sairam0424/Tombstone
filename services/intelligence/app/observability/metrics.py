"""RED (rate/errors/duration) HTTP metrics — the Python counterpart to the
six Go services' internal/telemetry.HTTPMetrics chi middleware (OBS-1).
Deliberately named app/observability/, not app/telemetry/: that package
already exists in this service for an unrelated concern (SDK evaluation-
event ingestion into ClickHouse, app/telemetry/routes.py +
clickhouse_writer.py) — reusing the name here would conflate two
unrelated kinds of "telemetry".

Uses prometheus_client directly rather than the OTel API + Prometheus
bridge the Go services use: this service has zero existing OpenTelemetry
usage (unlike the Go services, which already had OTel tracing wired before
OBS-1), so introducing the OTel Python SDK just to reach the same
Prometheus exposition format would be a heavier, less mature dependency
chain for no behavioral difference. The OBSERVABLE CONTRACT — metric
names, labels, and values — is identical to the Go services either way,
which is the actual cross-service goal (one comparable dashboard).
"""

from __future__ import annotations

import time
from typing import Callable

from prometheus_client import (
    CONTENT_TYPE_LATEST,
    REGISTRY,
    CollectorRegistry,
    Counter,
    Histogram,
    generate_latest,
)
from starlette.middleware.base import BaseHTTPMiddleware, RequestResponseEndpoint
from starlette.requests import Request
from starlette.responses import Response


def _route_label(request: Request) -> str:
    """Returns the matched route's TEMPLATE path (e.g. "/api/v1/anomaly/{flag_key}"),
    never the raw resolved path — the raw path could leak a real flag key
    (this endpoint is public, unauthenticated) or let an unauthenticated
    caller mint unlimited distinct path values, each consuming one slot of
    prometheus_client's registry (which, unlike a bounded OTel cardinality
    budget, has NO eviction at all — an unbounded label value here would
    grow forever, never just overflow into one bucket).

    request.scope["route"] is only populated once Starlette's router has
    matched a route — for a genuine 404, or for any upstream
    middleware/dependency that short-circuits before the router runs, it is
    never set. Callers MUST read this only after call_next(request) has
    returned (or raised) — reading it before would always see the
    pre-routing state.
    """
    route = request.scope.get("route")
    if route is None:
        return "unmatched"
    path = getattr(route, "path", None)
    return path or "unmatched"


def _status_class(status_code: int) -> str:
    return f"{status_code // 100}xx"


def build_red_metrics_dispatch(
    request_count: Counter, request_duration: Histogram
) -> Callable[[Request, RequestResponseEndpoint], "Response"]:
    """Builds a BaseHTTPMiddleware-compatible dispatch function bound to the
    given Counter/Histogram — mirrors the Go services' HTTPMetrics(meter),
    which likewise takes the metric instruments as a parameter rather than
    reaching for a hardcoded global, so tests can inject an isolated
    CollectorRegistry instead of sharing the process-wide default one.
    """

    async def dispatch(
        request: Request, call_next: RequestResponseEndpoint
    ) -> Response:
        start = time.perf_counter()

        def record(status_code: int) -> None:
            labels = {
                "route": _route_label(request),
                "method": request.method,
                "status": _status_class(status_code),
            }
            request_count.labels(**labels).inc()
            request_duration.labels(**labels).observe(time.perf_counter() - start)

        try:
            response = await call_next(request)
        except Exception:
            # An unhandled exception must still count as 5xx here rather
            # than silently skipping metrics for it — mirrors the Go
            # middleware's panic-then-record-then-repanic behavior exactly
            # (services/*/internal/telemetry/http_metrics.go).
            record(500)
            raise
        record(response.status_code)
        return response

    return dispatch


def _new_instruments(registry: CollectorRegistry) -> tuple[Counter, Histogram]:
    """Same base names as the Go services' HTTPMetrics — prometheus_client
    auto-suffixes "_total" on Counter and "_bucket"/"_sum"/"_count" on
    Histogram, so the exposed series names match byte-for-byte
    (http_server_requests_total, http_server_request_duration_seconds).
    """
    count = Counter(
        "http_server_requests",
        "Total HTTP requests received",
        ["route", "method", "status"],
        registry=registry,
    )
    duration = Histogram(
        "http_server_request_duration_seconds",
        "HTTP request duration in seconds",
        ["route", "method", "status"],
        registry=registry,
    )
    return count, duration


# Production instruments, backed by prometheus_client's process-wide
# default registry — created once at import time, exactly like the Go
# services' meter/counter/histogram construction happening once in
# InitMeter/HTTPMetrics.
_REQUEST_COUNT, _REQUEST_DURATION = _new_instruments(REGISTRY)


class RedMetricsMiddleware(BaseHTTPMiddleware):
    """Records RED metrics for every request against the default registry.
    Labeled by the matched ROUTE TEMPLATE (never the raw path) and a
    status-code CLASS (never the exact code) — see _route_label's
    docstring for why.
    """

    def __init__(self, app):
        super().__init__(
            app, dispatch=build_red_metrics_dispatch(_REQUEST_COUNT, _REQUEST_DURATION)
        )


def metrics_response() -> Response:
    """Renders the default registry in Prometheus exposition format, for
    mounting at GET /metrics — public, no auth middleware, matching /health.
    """
    return Response(content=generate_latest(REGISTRY), media_type=CONTENT_TYPE_LATEST)
