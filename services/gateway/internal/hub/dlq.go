package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	maxDeliveryAttempts  = 3
	reclaimIdleThreshold = 30 * time.Second
	dlqMaxLen            = 10_000 // mirrors the ClickHouse writer's DLQ_MAX shape (services/intelligence/app/telemetry/clickhouse_writer.py), different transport (Stream, not a capped list)

	// reclaimScanCount bounds how many PEL entries XPendingExt inspects per
	// sweep. This is a periodic maintenance pass, not the hot read path, so
	// a generous batch is fine — a busy stream is swept again in 15s anyway.
	reclaimScanCount = 100
)

// DLQStreamKey returns the dead-letter stream key for a given primary stream
// key. MUST stay byte-identical to the Python side's derivation
// (tombstone:stream:{environment} + ":dlq") — both the gateway and the
// intelligence service run independent consumer groups against the same
// primary stream, and both must file poison messages into the SAME dlq
// stream per environment so a human inspecting Redis sees one queue
// regardless of which service failed to process the message.
func DLQStreamKey(streamKey string) string {
	return streamKey + ":dlq"
}

// ReclaimStalePending scans streamKey's consumer-group Pending Entries List
// (PEL) via XPENDING (extended form), which returns idle-time and
// delivery-count per entry directly — no companion attempt-tracking hash is
// needed. For every entry idle beyond reclaimIdleThreshold:
//
//   - If its delivery count is below maxDeliveryAttempts, XCLAIM reassigns
//     the message to this sweep's consumer identity. Reassignment via
//     XCLAIM is NOT enough on its own to get the message reprocessed:
//     XREADGROUP's ">" cursor only ever returns messages that have never
//     been delivered to the group before. A message already in the PEL —
//     even after XCLAIM hands it to a new consumer — stays invisible to
//     RunStreamConsumer's normal ">" read loop; XCLAIM changes ownership
//     and resets idle time, it does not requeue the message for delivery.
//     (Confirmed against go-redis v9.5.1's stream_commands.go — XClaim
//     issues a bare `XCLAIM key group consumer min-idle-time id...` and
//     returns the claimed XMessage values directly, which is the only way
//     to get the message body back out; there is no re-delivery path
//     through XReadGroup.) So this function reprocesses the claimed message
//     itself inline (see reprocessClaimedMessage) rather than assuming
//     RunStreamConsumer will see it again.
//   - If delivery count is already >= maxDeliveryAttempts, the message is
//     declared dead: XADD it verbatim onto "<streamKey>:dlq" (capped at
//     dlqMaxLen, MaxLen+Approx:true, matching scheduler.go's publishToStream
//     XAdd convention), then XACK the original ID off the primary stream's
//     PEL. Redis 7 has no native "move to DLQ" primitive — this is the
//     explicit application-level decision that replaces it.
//
// GW-1: each replica now has its OWN consumer group, so a stream can have
// several groups' PELs to sweep, not one shared PEL. This enumerates every
// group on streamKey (via XINFO GROUPS) and reclaims within each — an
// entry stuck in a DEAD replica's group's PEL is exactly what needs
// reclaiming, and any live replica's sweep can XCLAIM/dead-letter within
// ANY group, not just its own (Redis Streams consumer groups aren't tied
// to a specific client connection).
func (b *Broadcaster) ReclaimStalePending(ctx context.Context, streamKey string) error {
	groups, err := b.rdb.XInfoGroups(ctx, streamKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil // stream doesn't exist yet — normal, not an error
		}
		return err
	}

	for _, g := range groups {
		if err := b.reclaimStalePendingInGroup(ctx, streamKey, g.Name); err != nil {
			b.logger.Warn("dlq: reclaim failed for group",
				zap.String("stream", streamKey), zap.String("group", g.Name), zap.Error(err))
		}
	}
	return nil
}

// reclaimStalePendingInGroup is ReclaimStalePending's per-group core.
func (b *Broadcaster) reclaimStalePendingInGroup(ctx context.Context, streamKey, group string) error {
	consumer := reclaimConsumerName()
	dlqKey := DLQStreamKey(streamKey)

	pending, err := b.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey,
		Group:  group,
		Idle:   reclaimIdleThreshold,
		Start:  "-",
		End:    "+",
		Count:  reclaimScanCount,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil // no pending entries at all — normal, not an error
		}
		return err
	}

	for _, entry := range pending {
		if entry.RetryCount >= maxDeliveryAttempts {
			if err := b.deadLetter(ctx, streamKey, group, dlqKey, entry.ID); err != nil {
				b.logger.Warn("dlq: failed to dead-letter message",
					zap.String("stream", streamKey),
					zap.String("group", group),
					zap.String("id", entry.ID),
					zap.Error(err))
			}
			continue
		}

		// Still under the attempt budget — reclaim ownership and re-process
		// immediately. XClaim returns the claimed messages' values directly,
		// so no second read call is needed to get the payload back.
		msgs, err := b.rdb.XClaim(ctx, &redis.XClaimArgs{
			Stream:   streamKey,
			Group:    group,
			Consumer: consumer,
			MinIdle:  reclaimIdleThreshold,
			Messages: []string{entry.ID},
		}).Result()
		if err != nil {
			b.logger.Warn("dlq: xclaim failed",
				zap.String("stream", streamKey),
				zap.String("group", group),
				zap.String("id", entry.ID),
				zap.Error(err))
			continue
		}

		for _, msg := range msgs {
			b.reprocessClaimedMessage(ctx, streamKey, group, msg)
		}
	}

	return nil
}

// deadLetter moves a poison message from the primary stream's PEL to its
// DLQ stream: XADD the raw fields onto the DLQ (capped, approximate trim —
// see publishToStream in services/flag-api/internal/scheduler/scheduler.go
// for the identical XAdd option shape), then XACK the original ID so it
// leaves the primary stream's PEL for good.
func (b *Broadcaster) deadLetter(ctx context.Context, streamKey, group, dlqKey, msgID string) error {
	// XPendingExt only returns PEL metadata (ID, consumer, idle, delivery
	// count) — re-read the message body via XRange before it's gone.
	msgs, err := b.rdb.XRange(ctx, streamKey, msgID, msgID).Result()
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		// Message already trimmed off the primary stream (MaxLen approx
		// eviction raced us) — nothing left to preserve. ACK to drop the
		// now-orphaned PEL entry.
		AckStreamMessage(ctx, b.rdb, streamKey, group, msgID)
		return nil
	}

	if err := b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqKey,
		MaxLen: dlqMaxLen,
		Approx: true,
		Values: msgs[0].Values,
	}).Err(); err != nil {
		return err
	}

	AckStreamMessage(ctx, b.rdb, streamKey, group, msgID)
	b.logger.Warn("dlq: message moved to dead-letter stream",
		zap.String("stream", streamKey),
		zap.String("group", group),
		zap.String("dlq", dlqKey),
		zap.String("id", msgID))
	return nil
}

// reprocessClaimedMessage re-runs the same unmarshal+broadcast+ack sequence
// RunStreamConsumer uses for freshly-read messages, applied to a message
// this sweep just reclaimed via XCLAIM. On repeated unmarshal failure it
// deliberately does NOT ack — the entry stays in the PEL, idle again, with
// delivery-count already incremented by XCLAIM, until a future sweep finds
// it over maxDeliveryAttempts and dead-letters it above.
func (b *Broadcaster) reprocessClaimedMessage(ctx context.Context, streamKey, group string, msg redis.XMessage) {
	payload, ok := msg.Values["payload"].(string)
	if !ok {
		b.logger.Warn("dlq: reclaimed message missing payload field, leaving pending",
			zap.String("stream", streamKey), zap.String("id", msg.ID))
		return
	}

	var event FlagEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		b.logger.Warn("dlq: reclaimed message still fails to unmarshal, leaving pending",
			zap.String("stream", streamKey), zap.String("id", msg.ID), zap.Error(err))
		return
	}

	environment, _ := msg.Values["environment"].(string)
	if environment == "" {
		environment = event.Environment
	}
	// GW-1 (dedup.go): a reclaimed message may still race a fresh delivery
	// of the same logical event via pub/sub or another replica's group.
	if b.deduper.claim(event) {
		b.hub.Broadcast(environment, event)
	}
	AckStreamMessage(ctx, b.rdb, streamKey, group, msg.ID)
}

// reclaimConsumerName builds a consumer identity distinct from
// RunStreamConsumer's own ("primary") so XPENDING/XCLAIM activity from the
// reclaim sweep is attributable separately in XINFO CONSUMERS — including
// when a live replica's sweep reclaims a DEAD replica's group, where this
// name shows up as a new consumer within a group it doesn't otherwise own.
func reclaimConsumerName() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("gateway-reclaim-%s", hostname)
}
