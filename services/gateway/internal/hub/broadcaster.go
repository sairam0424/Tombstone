package hub

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Broadcaster subscribes to Redis pub/sub and fans events into the Hub.
// Channel naming convention: stream:{environment}:updates
type Broadcaster struct {
	rdb    *redis.Client
	hub    *Hub
	logger *zap.Logger
	// group is this replica's own dedicated consumer group (GW-1) — set
	// once at construction, from this process's hostname.
	group   string
	deduper *eventDeduper
}

func NewBroadcaster(rdb *redis.Client, hub *Hub, logger *zap.Logger) *Broadcaster {
	hostname, _ := os.Hostname()
	return &Broadcaster{
		rdb:     rdb,
		hub:     hub,
		logger:  logger,
		group:   ReplicaGroupName(hostname),
		deduper: newEventDeduper(dedupWindow),
	}
}

// Group returns this replica's own dedicated consumer group name, for
// callers (cmd/main.go) that need it to seed the group or destroy it on
// shutdown.
func (b *Broadcaster) Group() string {
	return b.group
}

// Run starts the broadcaster. Reconnects with exponential backoff on failure.
// Should be called in a goroutine.
func (b *Broadcaster) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if err := b.subscribe(ctx); err != nil {
			if ctx.Err() != nil {
				return // context cancelled, clean shutdown
			}
			b.logger.Warn("broadcaster disconnected, reconnecting",
				zap.Error(err), zap.Duration("backoff", backoff))
			select {
			case <-time.After(JitterBackoff(backoff)):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second // reset on clean exit
	}
}

func (b *Broadcaster) subscribe(ctx context.Context) error {
	// PSUBSCRIBE matches all stream channels across all environments
	pubsub := b.rdb.PSubscribe(ctx, "stream:*:updates")
	defer pubsub.Close()

	b.logger.Info("broadcaster subscribed to Redis pub/sub")

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return redis.ErrClosed
			}
			b.handleMessage(msg)
		}
	}
}

func (b *Broadcaster) handleMessage(msg *redis.Message) {
	// Extract environment from channel name: "stream:{environment}:updates"
	parts := strings.Split(msg.Channel, ":")
	if len(parts) < 3 {
		b.logger.Warn("unexpected channel format", zap.String("channel", msg.Channel))
		return
	}
	environment := parts[1]

	var event FlagEvent
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		b.logger.Warn("failed to unmarshal flag event",
			zap.Error(err), zap.String("payload", msg.Payload))
		return
	}

	// GW-1 (dedup.go): the same logical event also arrives via this
	// replica's own Streams consumer group — suppress whichever copy
	// arrives second.
	if !b.deduper.claim(event) {
		return
	}
	// GW-2: pub/sub carries no durable, replayable position -- "" means
	// this frame gets no id: line.
	b.hub.Broadcast(environment, event, "")
}

// RunStreamConsumer reads from a Redis Stream consumer group for one environment.
// Runs concurrently with the pub/sub broadcaster (Run). Call in a goroutine per
// known environment. XACK is called after successful hub.Broadcast — not before.
func (b *Broadcaster) RunStreamConsumer(ctx context.Context, environment string) {
	streamKey := StreamKey(environment)
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := ReadStreamEvents(ctx, b.rdb, streamKey, b.group, replicaConsumerName)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.logger.Warn("stream read error, backing off",
				zap.String("stream", streamKey),
				zap.Error(err),
				zap.Duration("backoff", backoff))
			select {
			case <-time.After(JitterBackoff(backoff)):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, msg := range msgs {
			payload, ok := msg.Values["payload"].(string)
			if !ok {
				AckStreamMessage(ctx, b.rdb, streamKey, b.group, msg.ID)
				continue
			}
			var event FlagEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				// Do NOT ack — leave the message pending. A poison payload
				// deserves retries (it may be a transient producer bug) and,
				// failing that, a DLQ record — not a silent, permanent drop.
				// ReclaimStalePending (dlq.go), running on its own ticker in
				// cmd/main.go, decides this message's fate: XCLAIM + retry
				// while under maxDeliveryAttempts, or XADD to "<stream>:dlq"
				// + XACK once the attempt budget is exhausted.
				b.logger.Warn("stream: failed to unmarshal event, leaving pending for reclaim sweep",
					zap.Error(err), zap.String("id", msg.ID))
				continue
			}
			// GW-1 (dedup.go): the same logical event also arrives via the
			// legacy pub/sub path — suppress whichever copy arrives second.
			// GW-2: msg.ID is the real Redis-assigned stream entry ID for
			// this delivery -- pass it through so the SSE frame carries a
			// replayable id: line.
			if b.deduper.claim(event) {
				b.hub.Broadcast(environment, event, msg.ID)
			}
			AckStreamMessage(ctx, b.rdb, streamKey, b.group, msg.ID)
		}
	}
}
