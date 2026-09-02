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

	// replicaConsumerName is the sole consumer identity within a replica's
	// OWN consumer group (GW-1). Since each replica now owns a dedicated
	// group rather than competing with other replicas inside one shared
	// group, there is exactly one consumer per group, so a fixed name is
	// enough — the GROUP name (see ReplicaGroupName) is what actually
	// discriminates between replicas.
	replicaConsumerName = "primary"

	streamReadCount = 10
	streamBlockDur  = time.Second // 1 s block per XREADGROUP call
)

// StreamKey returns the Redis Streams key for an environment.
func StreamKey(environment string) string {
	return fmt.Sprintf("%s%s", StreamKeyPrefix, environment)
}

// ReplicaGroupName returns this replica's own dedicated consumer group name.
//
// GW-1: previously every gateway replica joined the SAME shared group
// ("gateway-workers") as different CONSUMER identities within it — the
// standard Redis Streams competing-consumers pattern, correct for a worker
// pool but wrong here: gateway needs FAN-OUT (every replica sees every
// message, to forward to its own distinct set of locally-connected SSE
// clients), not load-balancing (each message going to exactly one replica).
// Under the old shared group, only ~1/N of replicas' clients ever saw a
// given update via Streams — the legacy pub/sub path (still active,
// removal planned for v2.1) was the only thing making cross-replica
// delivery actually work. Giving each replica its own group means Streams
// alone now fans out correctly, matching pub/sub's guarantee — see dedup.go
// for how the two transports both being active without double-broadcasting
// to a replica's own clients is handled.
func ReplicaGroupName(hostname string) string {
	return fmt.Sprintf("gateway-workers-%s", hostname)
}

// CreateConsumerGroups creates groupName on each environment's stream,
// creating the stream itself if it does not exist (MKSTREAM). Idempotent —
// BUSYGROUP is a no-op.
func CreateConsumerGroups(ctx context.Context, rdb *redis.Client, environments []string, groupName string, logger *zap.Logger) {
	for _, env := range environments {
		key := StreamKey(env)
		err := rdb.XGroupCreateMkStream(ctx, key, groupName, "$").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			logger.Warn("xgroup create failed", zap.String("stream", key), zap.String("group", groupName), zap.Error(err))
		}
	}
}

// ReadStreamEvents reads up to streamReadCount messages from group as
// consumer, blocking up to streamBlockDur. Returns nil, nil on block
// timeout (normal).
func ReadStreamEvents(ctx context.Context, rdb *redis.Client, streamKey, group, consumer string) ([]redis.XMessage, error) {
	streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
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
func AckStreamMessage(ctx context.Context, rdb *redis.Client, streamKey, group, msgID string) {
	rdb.XAck(ctx, streamKey, group, msgID) //nolint:errcheck // fire-and-forget ack
}
