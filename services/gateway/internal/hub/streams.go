package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

	// replicaGroupPrefix identifies a consumer group as one of gateway's OWN
	// per-replica groups (see ReplicaGroupName). tombstone:stream:{env} is
	// NOT exclusive to gateway — services/intelligence's Python
	// RedisStreamsEventConsumer maintains its own, differently-named
	// long-lived group ("intelligence-worker") against the same stream keys.
	// Anything gateway does that enumerates/manages consumer groups on this
	// stream (GCIdleGroups) must filter to this prefix first, or it will act
	// on a group it doesn't own.
	replicaGroupPrefix = "gateway-workers-"

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
	return replicaGroupPrefix + hostname
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

// compareStreamIDs orders two Redis Stream entry IDs ("<unix-ms>-<seq>")
// numerically, not lexically. A plain string compare works today by
// accident (millisecond timestamps share a stable digit width for the
// practical future) but breaks the moment two IDs have different seq
// digit-widths (e.g. "5-9" vs "5-10" -- lexically "5-10" < "5-9"), so
// ReplaySince parses and compares both parts as integers instead of relying
// on that coincidence. Returns -1, 0, or 1 like strings.Compare.
func compareStreamIDs(a, b string) int {
	aMs, aSeq := splitStreamID(a)
	bMs, bSeq := splitStreamID(b)
	if aMs != bMs {
		if aMs < bMs {
			return -1
		}
		return 1
	}
	if aSeq != bSeq {
		if aSeq < bSeq {
			return -1
		}
		return 1
	}
	return 0
}

func splitStreamID(id string) (ms, seq uint64) {
	parts := strings.SplitN(id, "-", 2)
	ms, _ = strconv.ParseUint(parts[0], 10, 64)
	if len(parts) > 1 {
		seq, _ = strconv.ParseUint(parts[1], 10, 64)
	}
	return ms, seq
}

// ReplaySince returns the messages published to streamKey strictly after
// lastID (GW-2's reconnect catch-up). ok=false means lastID predates the
// oldest entry the stream currently retains -- entries between lastID and
// now were already trimmed by the producer's MaxLen/Approx eviction, so the
// caller must fall back to a full snapshot rather than a partial replay: an
// empty XRANGE result alone cannot distinguish "caught up, nothing missed"
// from "the gap was already lost to trimming".
func ReplaySince(ctx context.Context, rdb *redis.Client, streamKey, lastID string) (msgs []redis.XMessage, ok bool, err error) {
	oldest, err := rdb.XRangeN(ctx, streamKey, "-", "+", 1).Result()
	if err != nil {
		return nil, false, err
	}
	if len(oldest) > 0 && compareStreamIDs(lastID, oldest[0].ID) < 0 {
		return nil, false, nil
	}
	msgs, err = rdb.XRange(ctx, streamKey, "("+lastID, "+").Result()
	if err != nil {
		return nil, false, err
	}
	return msgs, true, nil
}

// BuildReplayFrames turns raw XRANGE results into ready-to-write SSE frames,
// applying the same unmarshal-payload and event-type derivation Broadcast
// uses for live delivery, so a replayed event is indistinguishable from one
// the client would have received live -- except it now carries its real
// stream ID as the id: line.
func BuildReplayFrames(msgs []redis.XMessage) [][]byte {
	frames := make([][]byte, 0, len(msgs))
	for _, msg := range msgs {
		payload, ok := msg.Values["payload"].(string)
		if !ok {
			continue
		}
		var event FlagEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		frames = append(frames, sseFrame(eventTypeFor(event), event, msg.ID))
	}
	return frames
}
