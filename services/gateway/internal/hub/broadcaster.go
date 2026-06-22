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

// knownEnvironments is the default set of environments for which consumer
// groups are initialised on startup. Additional environments discovered at
// runtime are handled by the streams consumer loop.
var knownEnvironments = []string{"development", "staging", "production"}

// Broadcaster subscribes to both Redis pub/sub (legacy, deprecated) and Redis
// Streams (primary, v2). It fans all events into the Hub for SSE delivery.
//
// Channel naming (pub/sub, deprecated): stream:{environment}:updates
// Stream key naming:                    tombstone:stream:{environment}
type Broadcaster struct {
	rdb      *redis.Client
	hub      *Hub
	logger   *zap.Logger
	consumer string // unique consumer name within the "gateway-workers" group
}

func NewBroadcaster(rdb *redis.Client, hub *Hub, logger *zap.Logger) *Broadcaster {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &Broadcaster{
		rdb:      rdb,
		hub:      hub,
		logger:   logger,
		consumer: fmt.Sprintf("gateway-%s", hostname),
	}
}

// Run starts the broadcaster. It launches both the legacy pub/sub subscriber
// and the new Redis Streams consumer concurrently. Each reconnects independently
// with exponential backoff on failure. Blocks until ctx is cancelled.
func (b *Broadcaster) Run(ctx context.Context) {
	// Initialise consumer groups for all known environments (idempotent).
	if err := CreateConsumerGroups(ctx, b.rdb, knownEnvironments); err != nil {
		b.logger.Warn("consumer group init partial failure", zap.Error(err))
	}

	// Legacy pub/sub fallback — runs concurrently with Streams consumer.
	go b.runPubSub(ctx)

	// Primary: Redis Streams consumers — one goroutine per environment.
	for _, env := range knownEnvironments {
		go b.runStreamConsumer(ctx, env)
	}

	// Block until context is done.
	<-ctx.Done()
}

// ---------------------------------------------------------------------------
// Redis pub/sub (legacy — deprecated; remove in v2.1)
// ---------------------------------------------------------------------------

// runPubSub is the legacy pub/sub subscriber. Reconnects with exponential
// backoff. Kept for one release cycle of backward compatibility.
func (b *Broadcaster) runPubSub(ctx context.Context) {
	backoff := time.Second
	for {
		if err := b.subscribe(ctx); err != nil {
			if ctx.Err() != nil {
				return // context cancelled, clean shutdown
			}
			b.logger.Warn("pubsub broadcaster disconnected, reconnecting",
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

	b.logger.Info("broadcaster subscribed to Redis pub/sub (deprecated fallback)")

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
		b.logger.Warn("failed to unmarshal flag event (pubsub)",
			zap.Error(err), zap.String("payload", msg.Payload))
		return
	}

	b.hub.Broadcast(environment, event)
}

// ---------------------------------------------------------------------------
// Redis Streams consumer (primary, v2)
// ---------------------------------------------------------------------------

// runStreamConsumer reads from tombstone:stream:{env} via the
// "gateway-workers" consumer group, broadcasts each event, then ACKs.
// Reconnects with exponential backoff on error.
func (b *Broadcaster) runStreamConsumer(ctx context.Context, env string) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := b.consumeStream(ctx, env); err != nil {
			if ctx.Err() != nil {
				return
			}
			b.logger.Warn("stream consumer error, reconnecting",
				zap.String("env", env), zap.Error(err), zap.Duration("backoff", backoff))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			// Re-create consumer group in case Redis restarted without persistence.
			if cErr := CreateConsumerGroups(ctx, b.rdb, []string{env}); cErr != nil {
				b.logger.Warn("consumer group re-init failed", zap.String("env", env), zap.Error(cErr))
			}
			continue
		}
		backoff = time.Second
	}
}

// consumeStream runs a single XREADGROUP loop for one environment until ctx
// is cancelled or a non-recoverable error occurs.
func (b *Broadcaster) consumeStream(ctx context.Context, env string) error {
	streamKey := fmt.Sprintf("tombstone:stream:%s", env)
	b.logger.Info("stream consumer started", zap.String("stream", streamKey), zap.String("consumer", b.consumer))

	// Deliver catch-up event to any SDKs that connected mid-stream.
	b.sendCatchUpEvent(ctx, env, streamKey)

	for {
		entries, err := ReadStreamEvents(ctx, b.rdb, env, b.consumer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("xreadgroup: %w", err)
		}

		for _, entry := range entries {
			b.handleStreamEntry(ctx, env, streamKey, entry)
		}
	}
}

// handleStreamEntry decodes a stream entry, broadcasts it to SSE clients, and
// acknowledges the message so it is not redelivered to this consumer group.
func (b *Broadcaster) handleStreamEntry(ctx context.Context, env, streamKey string, entry redis.XMessage) {
	payloadRaw, ok := entry.Values["payload"]
	if !ok {
		b.logger.Warn("stream entry missing payload field", zap.String("id", entry.ID))
		b.ackEntry(ctx, streamKey, entry.ID)
		return
	}

	payloadStr, _ := payloadRaw.(string)
	var event FlagEvent
	if err := json.Unmarshal([]byte(payloadStr), &event); err != nil {
		b.logger.Warn("failed to unmarshal stream event",
			zap.String("id", entry.ID), zap.Error(err))
		b.ackEntry(ctx, streamKey, entry.ID)
		return
	}

	// Ensure environment is populated even if the payload omits it.
	if event.Environment == "" {
		event.Environment = env
	}

	// Broadcast to all SSE clients subscribed to this environment.
	b.hub.Broadcast(event.Environment, event)

	// ACK only after successful broadcast so a crash before ACK causes redelivery.
	b.ackEntry(ctx, streamKey, entry.ID)
}

// ackEntry sends XACK and logs on failure (non-fatal — message will be
// redelivered but that is preferable to losing it).
func (b *Broadcaster) ackEntry(ctx context.Context, streamKey, msgID string) {
	if err := b.rdb.XAck(ctx, streamKey, "gateway-workers", msgID).Err(); err != nil {
		b.logger.Warn("xack failed", zap.String("stream", streamKey), zap.String("id", msgID), zap.Error(err))
	}
}

// sendCatchUpEvent fetches the most recent event via XREVRANGE and broadcasts
// it to any newly-connecting SSE clients so they don't miss the latest state.
func (b *Broadcaster) sendCatchUpEvent(ctx context.Context, env, streamKey string) {
	msgs, err := b.rdb.XRevRangeN(ctx, streamKey, "+", "-", 1).Result()
	if err != nil || len(msgs) == 0 {
		return // stream empty or Redis unavailable — not an error
	}

	payloadRaw, ok := msgs[0].Values["payload"]
	if !ok {
		return
	}
	payloadStr, _ := payloadRaw.(string)

	var event FlagEvent
	if err := json.Unmarshal([]byte(payloadStr), &event); err != nil {
		return
	}
	if event.Environment == "" {
		event.Environment = env
	}

	b.logger.Debug("catch-up event sent", zap.String("env", env), zap.String("msg_id", msgs[0].ID))
	b.hub.Broadcast(event.Environment, event)
}
