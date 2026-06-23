package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// StreamKeyPrefix is the Redis key prefix for all Tombstone flag event streams.
	StreamKeyPrefix = "tombstone:stream:"
	// ConsumerGroup is the consumer group name for all gateway instances.
	ConsumerGroup = "gateway-workers"

	streamReadCount = 10
	streamBlockDur  = time.Second // 1 s block per XREADGROUP call
)

// StreamKey returns the Redis Streams key for an environment.
func StreamKey(environment string) string {
	return fmt.Sprintf("%s%s", StreamKeyPrefix, environment)
}

// CreateConsumerGroups creates the consumer group on each stream, creating the
// stream itself if it does not exist (MKSTREAM). Idempotent — BUSYGROUP is a no-op.
func CreateConsumerGroups(ctx context.Context, rdb *redis.Client, environments []string, logger *zap.Logger) {
	for _, env := range environments {
		key := StreamKey(env)
		err := rdb.XGroupCreateMkStream(ctx, key, ConsumerGroup, "$").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			logger.Warn("xgroup create failed", zap.String("stream", key), zap.Error(err))
		}
	}
}

// ReadStreamEvents reads up to streamReadCount messages from the consumer group,
// blocking up to streamBlockDur. Returns nil, nil on block timeout (normal).
func ReadStreamEvents(ctx context.Context, rdb *redis.Client, streamKey, consumer string) ([]redis.XMessage, error) {
	streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumer,
		Streams:  []string{streamKey, ">"},
		Count:    streamReadCount,
		Block:    streamBlockDur,
	}).Result()
	if err == redis.Nil {
		return nil, nil // block timeout — normal, not an error
	}
	if err != nil {
		return nil, err
	}
	if len(streams) == 0 {
		return nil, nil
	}
	return streams[0].Messages, nil
}

// AckStreamMessage acknowledges a stream message after successful delivery.
func AckStreamMessage(ctx context.Context, rdb *redis.Client, streamKey, msgID string) {
	rdb.XAck(ctx, streamKey, ConsumerGroup, msgID) //nolint:errcheck // fire-and-forget ack
}
