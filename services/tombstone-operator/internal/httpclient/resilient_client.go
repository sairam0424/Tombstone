// Package httpclient wraps failsafe-go's execution pipeline (retry with
// exponential backoff + jitter, then an in-process circuit breaker) around
// outbound HTTP calls to other Tombstone services. Every inter-service HTTP
// call in this codebase was previously single-attempt: a momentary blip on
// the callee meant the caller just logged an error and gave up. This package
// fixes that without introducing a shared/vendored dependency — per this
// repo's established convention (see otel.go, duplicated byte-for-byte across
// services), this file is duplicated into each service's own internal/
// package rather than factored into a shared module.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

// ResilientClientConfig is per-call-site: e.g. a kill-switch caller wants fewer
// retries + a shorter circuit-breaker cooldown than a periodic sync caller.
type ResilientClientConfig struct {
	MaxRetries       uint          // e.g. 3
	InitialDelay     time.Duration // e.g. 200 * time.Millisecond
	MaxDelay         time.Duration // e.g. 5 * time.Second
	Timeout          time.Duration // per-attempt HTTP timeout, e.g. 10 * time.Second
	FailureThreshold uint          // consecutive failures before circuit opens, e.g. 5
	OpenDuration     time.Duration // how long circuit stays open, e.g. 30 * time.Second
}

// DefaultConfig returns sane defaults matching this platform's existing 5-15s
// timeout conventions found across the codebase.
func DefaultConfig() ResilientClientConfig {
	return ResilientClientConfig{
		MaxRetries:       3,
		InitialDelay:     200 * time.Millisecond,
		MaxDelay:         5 * time.Second,
		Timeout:          10 * time.Second,
		FailureThreshold: 5,
		OpenDuration:     30 * time.Second,
	}
}

// isRetryableFailure treats transport errors (DNS, connection refused,
// timeout) and 5xx responses as retryable/circuit-relevant failures. 4xx
// responses are client errors (bad request, auth, not-found) that a retry
// cannot fix, so they do NOT count as failures here.
func isRetryableFailure(resp *http.Response, err error) bool {
	return err != nil || (resp != nil && resp.StatusCode >= 500)
}

// ResilientClient wraps failsafe-go's execution pipeline (retry w/ exponential
// backoff + jitter -> circuit breaker) behind an http.Client-compatible Do
// method. One instance should be constructed per logical caller (e.g. one for
// a kill-switch path) and reused across requests so circuit-breaker state
// accumulates correctly across calls.
type ResilientClient struct {
	httpClient *http.Client
	executor   failsafe.Executor[*http.Response]
	logger     *zap.Logger
}

// NewResilientClient builds a ResilientClient. httpClient, if non-nil, is used
// as the underlying transport (so callers can keep a custom mTLS transport);
// otherwise a plain client with cfg.Timeout is constructed. If httpClient is
// provided with a zero Timeout, cfg.Timeout is applied to it.
func NewResilientClient(cfg ResilientClientConfig, httpClient *http.Client, logger *zap.Logger) *ResilientClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	} else if httpClient.Timeout == 0 {
		httpClient.Timeout = cfg.Timeout
	}
	// Every outbound call through this client previously started a
	// disconnected root span at the callee instead of continuing the
	// caller's trace — this client never touched Transport at all. Wraps
	// whatever is ALREADY on httpClient.Transport (or nil) as
	// otelhttp.NewTransport's base — nil is handled by otelhttp itself
	// (defaults to http.DefaultTransport). NOTE: this alone does not fully
	// fix tracing for THIS service — tombstone-operator has no
	// TracerProvider/propagator setup at all (no internal/telemetry
	// package, no otelhttp.NewHandler — it's a pure Kubernetes controller
	// with no inbound HTTP router), so outbound spans created here have no
	// registered exporter and the global propagator stays the OTel SDK's
	// no-op default. This wrap keeps the file consistent with every other
	// service's internal/httpclient copy and makes it immediately correct
	// if tracer-provider setup is ever added here — deliberately deferred,
	// not part of this fix (a new capability, not extending an existing
	// one).
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)
	if logger == nil {
		logger = zap.NewNop()
	}

	retryPolicy := retrypolicy.NewBuilder[*http.Response]().
		HandleIf(isRetryableFailure).
		WithMaxRetries(int(cfg.MaxRetries)).
		WithBackoff(cfg.InitialDelay, cfg.MaxDelay).
		WithJitterFactor(0.2).
		OnRetry(func(e failsafe.ExecutionEvent[*http.Response]) {
			logger.Warn("resilient client: retrying request",
				zap.Int("attempt", e.Attempts()),
				zap.Error(e.LastError()))
		}).
		Build()

	breaker := circuitbreaker.NewBuilder[*http.Response]().
		HandleIf(isRetryableFailure).
		WithFailureThreshold(cfg.FailureThreshold).
		WithDelay(cfg.OpenDuration).
		OnOpen(func(e circuitbreaker.StateChangedEvent) {
			logger.Error("resilient client: circuit breaker opened — failing fast",
				zap.String("previous_state", e.OldState.String()))
		}).
		OnClose(func(e circuitbreaker.StateChangedEvent) {
			logger.Info("resilient client: circuit breaker closed — resuming normal calls")
		}).
		Build()

	return &ResilientClient{
		httpClient: httpClient,
		executor:   failsafe.With[*http.Response](retryPolicy, breaker),
		logger:     logger,
	}
}

// Do executes req through the retry+circuit-breaker pipeline. Returns an error
// if all retries are exhausted or the circuit is open (circuitbreaker.ErrOpen).
//
// req is cloned per attempt via req.Clone, and the body is re-read via
// req.GetBody (populated automatically by http.NewRequest for common body
// types such as bytes.Reader/bytes.Buffer/strings.Reader) so retries resend
// the original payload rather than an exhausted/empty body.
func (c *ResilientClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := c.executor.GetWithExecution(func(exec failsafe.Execution[*http.Response]) (*http.Response, error) {
		attempt := req.Clone(ctx)
		if req.GetBody != nil {
			body, gbErr := req.GetBody()
			if gbErr != nil {
				return nil, fmt.Errorf("rewind request body for retry: %w", gbErr)
			}
			attempt.Body = body
		}
		return c.httpClient.Do(attempt)
	})
	if err != nil {
		return nil, fmt.Errorf("resilient client: %w", err)
	}
	return resp, nil
}
