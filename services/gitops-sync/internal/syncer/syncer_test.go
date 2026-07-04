package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/tombstone/gitops-sync/internal/parser"
)

// TestFlagExists_RetriesOnFlagAPIFailure verifies that a transient flag-api
// failure (HTTP 500) during flagExists is retried before Sync gives up on
// that flag definition.
func TestFlagExists_RetriesOnFlagAPIFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound) // flag doesn't exist yet
	}))
	defer srv.Close()

	s := NewSyncer(srv.URL, "test-token", zap.NewNop())
	exists, err := s.flagExists(context.Background(), "some-flag")
	if err != nil {
		t.Fatalf("flagExists() returned unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for 404 response")
	}
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Fatalf("expected at least 3 attempts (retried through transient 500s), got %d", got)
	}
}

// TestSync_CreatesNewFlagWhenNotFound verifies the end-to-end Sync path:
// flagExists returns false (404), so createFlag and updateEnvironment fire.
func TestSync_CreatesNewFlagWhenNotFound(t *testing.T) {
	var createCalled, updateCalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost:
			createCalled.Store(true)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch:
			updateCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	s := NewSyncer(srv.URL, "test-token", zap.NewNop())
	defs := []*parser.FlagDefinition{
		{
			Metadata: parser.FlagMetadata{Name: "Test Flag", Owner: "team-x"},
			Spec: parser.FlagSpec{
				Key:  "test-flag",
				Type: "boolean",
				Environments: map[string]parser.EnvSpec{
					"production": {Enabled: true, RolloutPct: 100},
				},
			},
		},
	}

	result := s.Sync(context.Background(), defs, "proj-1")
	if len(result.Errors) != 0 {
		t.Fatalf("Sync() returned errors: %v", result.Errors)
	}
	if len(result.Created) != 1 || result.Created[0] != "test-flag" {
		t.Fatalf("expected Created=[test-flag], got %v", result.Created)
	}
	if !createCalled.Load() {
		t.Error("expected createFlag to be called")
	}
	if !updateCalled.Load() {
		t.Error("expected updateEnvironment to be called")
	}
}
