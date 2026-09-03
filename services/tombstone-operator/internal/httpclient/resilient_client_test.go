package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// TestResilientClient_RetriesUpToMaxRetries verifies that a handler which
// always fails (HTTP 500) is retried MaxRetries times before giving up.
func TestResilientClient_RetriesUpToMaxRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.MaxRetries = 3
	cfg.InitialDelay = 1 * time.Millisecond
	cfg.MaxDelay = 2 * time.Millisecond
	cfg.FailureThreshold = 100 // keep breaker from tripping mid-test

	c := NewResilientClient(cfg, nil, zap.NewNop())

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, doErr := c.Do(req.Context(), req)
	if resp != nil {
		resp.Body.Close()
	}
	if doErr == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 4 {
		t.Fatalf("expected 4 attempts (1 initial + 3 retries), got %d", got)
	}
}

// TestResilientClient_CircuitOpensAfterConsecutiveFailures verifies that
// after enough consecutive failures the circuit opens and subsequent calls
// fail fast without attempting the HTTP call at all.
func TestResilientClient_CircuitOpensAfterConsecutiveFailures(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.MaxRetries = 0 // isolate breaker behavior from retry behavior
	cfg.FailureThreshold = 3
	cfg.OpenDuration = time.Minute

	c := NewResilientClient(cfg, nil, zap.NewNop())

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, _ := c.Do(req.Context(), req)
		if resp != nil {
			resp.Body.Close()
		}
	}
	before := atomic.LoadInt32(&attempts)
	if before != 3 {
		t.Fatalf("expected 3 real attempts to trip breaker, got %d", before)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, doErr := c.Do(req.Context(), req)
	if resp != nil {
		resp.Body.Close()
	}
	if doErr == nil {
		t.Fatal("expected circuit-open error, got nil")
	}
	after := atomic.LoadInt32(&attempts)
	if after != before {
		t.Fatalf("expected no new server hit while circuit open: before=%d after=%d", before, after)
	}
}

// TestResilientClient_SucceedsWithoutRetryOnFirstSuccess is the happy-path
// control: a healthy upstream should be called exactly once.
func TestResilientClient_SucceedsWithoutRetryOnFirstSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewResilientClient(DefaultConfig(), nil, zap.NewNop())
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt on success, got %d", got)
	}
}

// TestResilientClient_RewindsBodyOnRetry verifies each retry attempt resends
// the original request body rather than an exhausted/empty one.
func TestResilientClient_RewindsBodyOnRetry(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, string(buf[:n]))
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.MaxRetries = 2
	cfg.InitialDelay = 1 * time.Millisecond
	cfg.MaxDelay = 2 * time.Millisecond
	cfg.FailureThreshold = 100

	c := NewResilientClient(cfg, nil, zap.NewNop())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload-body"))
	resp, _ := c.Do(req.Context(), req)
	if resp != nil {
		resp.Body.Close()
	}

	if len(bodies) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(bodies))
	}
	for i, b := range bodies {
		if b != "payload-body" {
			t.Fatalf("attempt %d: expected body 'payload-body', got %q", i, b)
		}
	}
}

// TestResilientClient_PropagatesTraceContext is the direct regression proof
// for the outbound-trace-propagation fix: a request made through
// ResilientClient, from a context carrying an active span, must inject W3C
// tracecontext propagation headers into the actual outbound request.
// tombstone-operator has no TracerProvider/propagator setup of its own
// today (no internal/telemetry package — see resilient_client.go's doc
// comment), so this proves the underlying primitive works correctly and
// is ready to use the moment tracer-provider setup is added here; it does
// NOT prove tombstone-operator's real outbound calls are traced today.
//
// otel.SetTextMapPropagator is set explicitly here rather than relying on
// package-level global state: this test's process is its own `go test`
// binary, so nothing else in THIS binary would otherwise ever call it —
// the OTel SDK's global propagator defaults to a no-op until something
// sets it, regardless of whether the transport itself is wrapped.
func TestResilientClient_PropagatesTraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer provider.Shutdown(context.Background()) //nolint:errcheck
	tracer := provider.Tracer("test")

	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()
	wantTraceID := span.SpanContext().TraceID().String()

	c := NewResilientClient(DefaultConfig(), nil, zap.NewNop())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, doErr := c.Do(ctx, req)
	if doErr != nil {
		t.Fatalf("unexpected error: %v", doErr)
	}
	resp.Body.Close()

	if gotTraceparent == "" {
		t.Fatal("outbound request carried no traceparent header — the trace was not propagated")
	}
	if !strings.Contains(gotTraceparent, wantTraceID) {
		t.Errorf("traceparent %q does not carry the caller's trace ID %q — this is a disconnected root span, not a continuation of the caller's trace", gotTraceparent, wantTraceID)
	}
}

// recordingTransport is a minimal http.RoundTripper that proves it was
// actually invoked, standing in for a real caller-supplied custom transport.
type recordingTransport struct {
	called bool
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.called = true
	return http.DefaultTransport.RoundTrip(req)
}

// TestResilientClient_WrapsRatherThanReplacesCustomTransport proves the
// otelhttp wrapping does not silently discard a caller-supplied custom
// transport — it must wrap it as the base RoundTripper, not replace it
// outright.
func TestResilientClient_WrapsRatherThanReplacesCustomTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	custom := &recordingTransport{}
	httpClient := &http.Client{Transport: custom}

	c := NewResilientClient(DefaultConfig(), httpClient, zap.NewNop())

	if _, ok := httpClient.Transport.(*otelhttp.Transport); !ok {
		t.Fatalf("httpClient.Transport is %T after NewResilientClient, want *otelhttp.Transport — the custom transport was never wrapped", httpClient.Transport)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if !custom.called {
		t.Error("the caller-supplied custom transport was never invoked — otelhttp.NewTransport must wrap it as base, not discard it")
	}
}
