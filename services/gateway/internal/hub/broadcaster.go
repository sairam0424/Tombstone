package hub

import (
	"context"
	"encoding/json"
	"fmt"
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
}

func NewBroadcaster(rdb *redis.Client, hub *Hub, logger *zap.Logger) *Broadcaster {
	return &Broadcaster{rdb: rdb, hub: hub, logger: logger}
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

	b.hub.Broadcast(environment, event)
}

// RunStreamConsumer reads from a Redis Stream consumer group for one environment.
// Runs concurrently with the pub/sub broadcaster (Run). Call in a goroutine per
// known environment. XACK is called after successful hub.Broadcast — not before.
func (b *Broadcaster) RunStreamConsumer(ctx context.Context, environment string) {
	hostname, _ := os.Hostname()
	consumer := fmt.Sprintf("gateway-%s", hostname)
	streamKey := StreamKey(environment)
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := ReadStreamEvents(ctx, b.rdb, streamKey, consumer)
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
				AckStreamMessage(ctx, b.rdb, streamKey, msg.ID)
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
			b.hub.Broadcast(environment, event)
			AckStreamMessage(ctx, b.rdb, streamKey, msg.ID)
		}
	}
}
