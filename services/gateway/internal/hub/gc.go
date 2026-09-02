package hub

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// groupIdleGCThreshold bounds how long a per-replica consumer group's
// consumer(s) can go unseen before the group is presumed abandoned — its
// owning replica crashed, was OOMKilled, or scaled down without running
// the graceful-shutdown destroy (see cmd/main.go's shutdown hook). A live,
// healthy replica's RunStreamConsumer loop touches its group via
// XReadGroup at least once per streamBlockDur (1s) regardless of message
// traffic, so a live replica's idle time never approaches this threshold —
// it's set far larger purely to tolerate scheduling/network jitter, not
// because a live replica is ever expected to come close.
const groupIdleGCThreshold = 5 * time.Minute

// GCIdleGroups enumerates every consumer group on streamKey and destroys
// any whose consumer(s) have all gone quiet longer than
// groupIdleGCThreshold — nothing will ever read from an abandoned group
// again, so its PEL and bookkeeping would otherwise linger in Redis
// forever. Safe to call from any live replica; the on-shutdown destroy in
// cmd/main.go handles the common case (rolling deploys, deliberate
// scale-down) immediately — this is the backstop for ungraceful deaths.
// A group with zero registered consumers (created but never read from) is
// also treated as stale and destroyed.
func GCIdleGroups(ctx context.Context, rdb *redis.Client, streamKey string, logger *zap.Logger) {
	groups, err := rdb.XInfoGroups(ctx, streamKey).Result()
	if err != nil {
		if err != redis.Nil {
			logger.Warn("gc: xinfo groups failed", zap.String("stream", streamKey), zap.Error(err))
		}
		return
	}

	for _, g := range groups {
		// tombstone:stream:{env} is NOT exclusive to gateway — the Python
		// intelligence service maintains its own, differently-named
		// long-lived group ("intelligence-worker") against the same stream
		// keys. GC must never even consider destroying a group it doesn't
		// own; anything not matching gateway's own naming convention is
		// somebody else's, and its liveness is not this function's call.
		if !strings.HasPrefix(g.Name, replicaGroupPrefix) {
			continue
		}

		consumers, err := rdb.XInfoConsumers(ctx, streamKey, g.Name).Result()
		if err != nil {
			logger.Warn("gc: xinfo consumers failed",
				zap.String("stream", streamKey), zap.String("group", g.Name), zap.Error(err))
			continue
		}

		// Only the group's own "primary" consumer (streams.go's
		// replicaConsumerName) counts as a liveness signal. dlq.go's
		// cross-group reclaim registers a DIFFERENT consumer identity
		// ("gateway-reclaim-{hostname}") inside foreign groups every time it
		// successfully XCLAIMs an entry out of one — that consumer's fresh
		// idle time must not spare an otherwise-abandoned group from GC just
		// because it still has reclaimable backlog; only its actual owning
		// replica's own continued activity should.
		stale := true
		for _, c := range consumers {
			if c.Name != replicaConsumerName {
				continue
			}
			if c.Idle < groupIdleGCThreshold {
				stale = false
				break
			}
		}
		if !stale {
			continue
		}

		if err := rdb.XGroupDestroy(ctx, streamKey, g.Name).Err(); err != nil {
			logger.Warn("gc: xgroup destroy failed",
				zap.String("stream", streamKey), zap.String("group", g.Name), zap.Error(err))
			continue
		}
		logger.Info("gc: destroyed idle consumer group",
			zap.String("stream", streamKey), zap.String("group", g.Name))
	}
}
