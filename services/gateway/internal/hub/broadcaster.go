package hub

import (
	"context"
	"encoding/json"
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
			case <-time.After(backoff):
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
