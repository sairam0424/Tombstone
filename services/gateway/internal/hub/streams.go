package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	streamConsumerGroup = "gateway-workers"
	streamReadCount     = 10
	streamBlockDuration = time.Second // BLOCK 1000ms per XREADGROUP call
)

// CreateConsumerGroups idempotently creates the "gateway-workers" consumer
// group for each supplied environment stream. The group starts at "$" (only
// new messages) with MKSTREAM so the stream is created even if no events have
// been published yet.
//
// Safe to call on startup repeatedly — XGROUP CREATE returns BUSYGROUP if the
// group already exists; this is treated as a no-op.
func CreateConsumerGroups(ctx context.Context, rdb *redis.Client, environments []string) error {
	var lastErr error
	for _, env := range environments {
		streamKey := fmt.Sprintf("tombstone:stream:%s", env)
		err := rdb.XGroupCreateMkStream(ctx, streamKey, streamConsumerGroup, "$").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			// Log but continue so one bad environment doesn't block the others.
			lastErr = fmt.Errorf("xgroup create %s: %w", streamKey, err)
		}
	}
	return lastErr
}

// ReadStreamEvents performs a single XREADGROUP call for the given environment,
// blocking up to streamBlockDuration for new messages. Returns the slice of
// messages delivered (may be empty on timeout, which is normal).
//
// The caller is responsible for ACKing each message after processing via
// rdb.XAck(ctx, streamKey, streamConsumerGroup, msgID).
func ReadStreamEvents(ctx context.Context, rdb *redis.Client, env, consumer string) ([]redis.XMessage, error) {
	streamKey := fmt.Sprintf("tombstone:stream:%s", env)

	streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    streamConsumerGroup,
		Consumer: consumer,
		Streams:  []string{streamKey, ">"},
		Count:    streamReadCount,
		Block:    streamBlockDuration,
	}).Result()
	if err != nil {
		// redis.Nil is returned when BLOCK times out with no messages — not an error.
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	if len(streams) == 0 {
		return nil, nil
	}
	return streams[0].Messages, nil
}

// StreamConsumer is a self-contained helper that drives a continuous
// XREADGROUP loop for a single environment, sending decoded FlagEvents on the
// returned channel. The caller must drain the channel; the goroutine exits
// when ctx is cancelled.
//
// This is a convenience wrapper for callers that prefer a channel-based API
// over the raw ReadStreamEvents function (e.g. integration tests).
func StreamConsumer(ctx context.Context, rdb *redis.Client, env, consumer string, logger *zap.Logger) <-chan FlagEvent {
	out := make(chan FlagEvent, 64)

	go func() {
		defer close(out)
		streamKey := fmt.Sprintf("tombstone:stream:%s", env)

		for {
			if ctx.Err() != nil {
				return
			}

			msgs, err := ReadStreamEvents(ctx, rdb, env, consumer)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Warn("stream consumer read error",
					zap.String("stream", streamKey), zap.Error(err))
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					return
				}
				continue
			}

			for _, msg := range msgs {
				payloadRaw, ok := msg.Values["payload"]
				if !ok {
					rdb.XAck(ctx, streamKey, streamConsumerGroup, msg.ID) //nolint:errcheck
					continue
				}
				payloadStr, _ := payloadRaw.(string)

				var event FlagEvent
				if err := json.Unmarshal([]byte(payloadStr), &event); err != nil {
					logger.Warn("stream: unmarshal error",
						zap.String("id", msg.ID), zap.Error(err))
					rdb.XAck(ctx, streamKey, streamConsumerGroup, msg.ID) //nolint:errcheck
					continue
				}
				if event.Environment == "" {
					event.Environment = env
				}

				select {
				case out <- event:
				case <-ctx.Done():
					return
				}

				// ACK after sending to channel; if the goroutine is killed between
				// send and ACK the message will be redelivered (at-least-once).
				rdb.XAck(ctx, streamKey, streamConsumerGroup, msg.ID) //nolint:errcheck
			}
		}
	}()

	return out
}
