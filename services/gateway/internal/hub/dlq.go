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
// entry stuck in a DEAD replica's group's PEL still needs draining (acked
// or dead-lettered) so it doesn't linger forever, and any live replica's
// sweep can XCLAIM within ANY group, not just its own (Redis Streams
// consumer groups aren't tied to a specific client connection). Draining a
// FOREIGN group's entry never re-broadcasts it, though — see
// reprocessClaimedMessage's doc comment for why redelivering a dead
// replica's abandoned copy would be a pure duplicate, not a recovery.
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
		// Claim ownership FIRST, before acting on the entry either way.
		// XCLAIM's MinIdle filter is atomic — once one caller claims an ID
		// its idle time resets to 0, so a second concurrent caller's claim
		// for the same ID (same MinIdle filter) returns nothing for it. This
		// used to guard only the retry branch below; the dead-letter branch
		// acted directly off this plain XPendingExt read with no claim step
		// in between, so two live replicas' independently-ticking reclaim
		// sweeps (or this replica's own sweep racing another's, since GW-1
		// makes cross-group reclaim routine — see ReclaimStalePending's doc
		// comment) could both see the same over-budget entry and both
		// dead-letter it, producing a duplicate DLQ entry for one poison
		// message. Claiming first, uniformly, closes that gap: whichever
		// sweep loses the claim race gets zero messages back and does
		// nothing further for this ID.
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
		if len(msgs) == 0 {
			// Lost the claim race to a concurrent sweep, or the message was
			// trimmed off the stream — nothing left for this sweep to do.
			continue
		}

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

		for _, msg := range msgs {
			b.reprocessClaimedMessage(ctx, streamKey, group, group == b.group, msg)
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
//
// isOwnGroup distinguishes reclaiming THIS replica's own group (group ==
// b.group) from a FOREIGN group belonging to some other replica.
// Under GW-1's per-replica fan-out, a message published to the stream is
// independently delivered to EVERY live replica's own group — so if a
// DIFFERENT replica dies after XReadGroup but before broadcasting/acking,
// THIS replica's own group has almost certainly already delivered and
// broadcast the identical event to this replica's own clients within ~1s of
// publish, via the normal RunStreamConsumer path. Re-broadcasting a foreign
// group's stuck entry would therefore be a genuine duplicate to this
// replica's already-served clients, not a recovery of anything: the dead
// replica that owned that group has no SSE clients left to redeliver to —
// they disconnected when it died. Foreign-group reclaim exists purely to
// drain that group's PEL (ack or dead-letter) so it doesn't linger forever;
// it must never redeliver. Only a self-reclaim (this replica's OWN group
// stuck on itself — e.g. a transient ack failure, not a crash) still needs
// to broadcast, since in that case THIS replica's clients may genuinely
// never have received it.
func (b *Broadcaster) reprocessClaimedMessage(ctx context.Context, streamKey, group string, isOwnGroup bool, msg redis.XMessage) {
	payload, ok := msg.Values["payload"].(string)
	if !ok {
		b.logger.Warn("dlq: reclaimed message missing payload field, leaving pending",
			zap.String("stream", streamKey), zap.String("id", msg.ID))
		return
	}

	// Same "kind" discriminator RunStreamConsumer's live path and
	// BuildReplayFrames' reconnect-replay path both check -- found missing
	// here by adversarial review of the prerequisites-streaming PR: without
	// it, a prerequisites_updated entry that ever sits in the PEL past
	// reclaimIdleThreshold (backpressure, a transient Redis hiccup, or a
	// crash mid-processing) would silently unmarshal into a FlagEvent with
	// every field at its zero value (enabled=false, rollout_pct=0,
	// reason="") -- Go's json.Unmarshal ignores the payload's real
	// "prerequisites" key and leaves FlagEvent's missing keys zeroed rather
	// than erroring -- and get rebroadcast as a bogus "flag disabled,
	// rollout 0%" event to every connected client.
	if kind, _ := msg.Values["kind"].(string); kind == "prerequisites_updated" {
		if isOwnGroup {
			environment, _ := msg.Values["environment"].(string)
			// No eventDeduper.claim here, matching RunStreamConsumer's own
			// prerequisites_updated branch: this event kind is never
			// dual-written to the legacy pub/sub path, so there is nothing
			// to dedupe against.
			b.hub.BroadcastRaw(environment, kind, []byte(payload), msg.ID)
		}
		AckStreamMessage(ctx, b.rdb, streamKey, group, msg.ID)
		return
	}

	var event FlagEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		b.logger.Warn("dlq: reclaimed message still fails to unmarshal, leaving pending",
			zap.String("stream", streamKey), zap.String("id", msg.ID), zap.Error(err))
		return
	}

	if isOwnGroup {
		environment, _ := msg.Values["environment"].(string)
		if environment == "" {
			environment = event.Environment
		}
		// GW-1 (dedup.go): a self-reclaimed message may still race a fresh
		// delivery of the same logical event via pub/sub.
		// GW-2: this is a genuine stream entry (reclaimed via XCLAIM), so
		// msg.ID is a real, replayable position -- pass it through.
		if b.deduper.claim(event) {
			b.hub.Broadcast(environment, event, msg.ID)
		}
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
