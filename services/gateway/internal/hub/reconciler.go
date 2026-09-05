// Package hub — reconciler.go implements a low-frequency belt-and-suspenders
// reconciliation loop for the dual-write gap between flag-api's Postgres
// commit and its separate, non-transactional Redis publish/XADD calls
// (see services/flag-api/internal/api/v1/flags.go publishEvent/publishToStream).
//
// If flag-api crashes or Redis is briefly unreachable between the DB commit
// and the publish, that event's real-time SSE notification is lost forever —
// though the durable record (audit_log) is never at risk, and the next
// snapshot fetch (SDK reconnect, or this reconciler) will pick up the true
// state. This reconciler polls flag-api's snapshot endpoint every 5 minutes
// per known environment, diffs it against the hub's last-known snapshot, and
// broadcasts deltas so any connected SSE clients converge even if they missed
// the original Redis event. This is cheap insurance, not a primary delivery
// path — it runs ALONGSIDE the existing Broadcaster.Run/RunStreamConsumer
// goroutines, never instead of them.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/gateway/internal/httpclient"
)

// reconcileInterval is how often the reconciler polls flag-api per environment.
const reconcileInterval = 5 * time.Minute

// snapshotFlag mirrors the subset of flag-api's snapshot response this
// reconciler needs. It intentionally does not need prerequisites or full
// FlagEnvironmentStateWithPrereqs — just enough to detect drift and rebroadcast.
type snapshotFlag struct {
	FlagKey     string `json:"flag_key"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	UpdatedAt   int64  `json:"updated_at"`
}

type snapshotResponse struct {
	Environment string         `json:"environment"`
	Flags       []snapshotFlag `json:"flags"`
	Hash        string         `json:"hash"`
	Ts          int64          `json:"ts"`
}

// Reconciler periodically fetches flag-api's full snapshot per environment
// and rebroadcasts any flags whose state differs from what the hub last saw.
type Reconciler struct {
	hub          *Hub
	client       *httpclient.ResilientClient
	flagAPIURL   string
	flagAPIToken string
	logger       *zap.Logger
}

// NewReconciler builds a Reconciler. flagAPIToken is sent as a Bearer token
// to flag-api's snapshot endpoint, matching the convention used by the
// evaluator's rollback executor.
func NewReconciler(h *Hub, flagAPIURL, flagAPIToken string, logger *zap.Logger) *Reconciler {
	cfg := httpclient.DefaultConfig()
	return &Reconciler{
		hub:          h,
		client:       httpclient.NewResilientClient(cfg, nil, logger),
		flagAPIURL:   flagAPIURL,
		flagAPIToken: flagAPIToken,
		logger:       logger,
	}
}

// Run starts the reconciliation loop for the given environments. Blocks until
// ctx is cancelled — call as "go reconciler.Run(ctx, knownEnvs)".
func (r *Reconciler) Run(ctx context.Context, environments []string) {
	r.logger.Info("snapshot reconciler starting",
		zap.Duration("interval", reconcileInterval),
		zap.Strings("environments", environments))

	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("snapshot reconciler stopped")
			return
		case <-ticker.C:
			for _, env := range environments {
				r.reconcileOne(ctx, env)
			}
		}
	}
}

// reconcileOne fetches the snapshot for one environment and rebroadcasts any
// flags whose state has changed since the last poll. Correctness over
// cleverness: this compares full per-flag state (not a precise field-level
// diff) — if a flag's enabled/rollout_pct/updated_at changed, it is
// rebroadcast in full.
func (r *Reconciler) reconcileOne(ctx context.Context, environment string) {
	snap, raw, err := r.fetchSnapshot(ctx, environment)
	if err != nil {
		r.logger.Warn("reconciler: snapshot fetch failed",
			zap.String("env", environment), zap.Error(err))
		return
	}

	prevRaw, hadPrev := r.hub.LastSnapshot(environment)
	r.hub.SetLastSnapshot(environment, raw)

	if !hadPrev {
		// First poll for this environment — nothing to diff against yet.
		return
	}
	if string(prevRaw) == string(raw) {
		return // identical — no drift, nothing to notify.
	}

	prev, err := decodeSnapshot(prevRaw)
	if err != nil {
		// Can't decode the previous snapshot (shouldn't happen since we wrote
		// it ourselves) — fail safe by rebroadcasting everything in the new one.
		r.logger.Warn("reconciler: failed to decode previous snapshot; rebroadcasting all",
			zap.String("env", environment), zap.Error(err))
		for _, f := range snap.Flags {
			r.broadcastDrift(environment, f)
		}
		return
	}

	prevByKey := make(map[string]snapshotFlag, len(prev.Flags))
	for _, f := range prev.Flags {
		prevByKey[f.FlagKey] = f
	}

	for _, f := range snap.Flags {
		old, existed := prevByKey[f.FlagKey]
		if !existed || old != f {
			r.broadcastDrift(environment, f)
		}
	}
}

// broadcastDrift notifies connected SSE clients of a flag whose state drifted
// from the hub's last-known snapshot without a corresponding Redis event
// having been observed.
func (r *Reconciler) broadcastDrift(environment string, f snapshotFlag) {
	r.logger.Info("reconciler: drift detected, rebroadcasting",
		zap.String("env", environment),
		zap.String("flag", f.FlagKey),
		zap.Bool("enabled", f.Enabled),
		zap.Int("rollout_pct", f.RolloutPct))

	// GW-2: a synthetic drift correction, not a real stream entry -- no ID.
	r.hub.Broadcast(environment, FlagEvent{
		FlagKey:     f.FlagKey,
		Enabled:     f.Enabled,
		RolloutPct:  f.RolloutPct,
		Reason:      "reconciler_drift",
		Ts:          time.Now().Unix(),
		Environment: environment,
	}, "")
}

// fetchSnapshot calls flag-api's snapshot endpoint via the resilient client
// and returns both the decoded response and the raw JSON bytes (the raw
// bytes are what gets stored as the hub's "last known snapshot" for the next
// diff, avoiding any re-serialization drift).
func (r *Reconciler) fetchSnapshot(ctx context.Context, environment string) (*snapshotResponse, []byte, error) {
	url := fmt.Sprintf("%s/api/v1/environments/snapshot?environment=%s", r.flagAPIURL, environment)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build snapshot request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.flagAPIToken)

	resp, err := r.client.Do(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("snapshot request returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read snapshot body: %w", err)
	}

	snap, err := decodeSnapshot(raw)
	if err != nil {
		return nil, nil, err
	}
	return snap, raw, nil
}

func decodeSnapshot(raw []byte) (*snapshotResponse, error) {
	var snap snapshotResponse
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return &snap, nil
}
