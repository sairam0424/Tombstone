package v1

import (
	"os"
	"regexp"
	"testing"
)

// TestMutationHandlersShareOneFlagEventAcrossBothTransports is a structural
// regression guard, mirroring gateway's cmd/main_test.go technique: the fix
// this pins — UpdateEnvironment, KillSwitch, and ApproveChangeRequest each
// constructing ONE FlagEvent value and passing that SAME value to both the
// pub/sub publish call and the Streams publish call, instead of two
// independently-constructed literals with two separate time.Now().Unix()
// calls — can't be exercised through the real handlers without a live DB
// (BeginTx/ExecContext), so this parses the source instead.
//
// Why this matters: gateway's eventDeduper (services/gateway/internal/hub/
// dedup.go) keys on the full FlagEvent struct including Ts (second
// precision). If a future edit re-split the shared value back into two
// independently-timestamped literals, no compile error or DB-level test
// would catch it — but two time.Now().Unix() calls straddling a whole-second
// boundary would make gateway's dedup treat the pub/sub copy and the
// streams copy of the SAME mutation as two different events, double-
// broadcasting to every gateway replica's clients for that narrow window.
func TestMutationHandlersShareOneFlagEventAcrossBothTransports(t *testing.T) {
	flagsSrc, err := os.ReadFile("flags.go")
	if err != nil {
		t.Fatalf("read flags.go: %v", err)
	}
	changeRequestsSrc, err := os.ReadFile("change_requests.go")
	if err != nil {
		t.Fatalf("read change_requests.go: %v", err)
	}

	cases := []struct {
		name    string
		src     string
		declare string
		pubsub  string
		stream  string
	}{
		{
			name:    "UpdateEnvironment",
			src:     string(flagsSrc),
			declare: `event\s*:=\s*FlagEvent\{`,
			pubsub:  `h\.publishEvent\(r\.Context\(\),\s*env,\s*event\)`,
			stream:  `h\.publishToStream\(r\.Context\(\),\s*env,\s*event\)`,
		},
		{
			name:    "KillSwitch",
			src:     string(flagsSrc),
			declare: `killEvent\s*:=\s*FlagEvent\{`,
			pubsub:  `h\.publishEvent\(r\.Context\(\),\s*req\.Environment,\s*killEvent\)`,
			stream:  `h\.publishToStream\(r\.Context\(\),\s*req\.Environment,\s*killEvent\)`,
		},
		{
			name:    "ApproveChangeRequest",
			src:     string(changeRequestsSrc),
			declare: `applyEvent\s*:=\s*FlagEvent\{`,
			pubsub:  `publishFlagEvent\(r\.Context\(\),\s*h\.rdb,\s*h\.logger,\s*cr\.Environment,\s*applyEvent\)`,
			stream:  `publishFlagEventToStream\(r\.Context\(\),\s*h\.rdb,\s*h\.logger,\s*cr\.Environment,\s*applyEvent\)`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !regexp.MustCompile(c.declare).MatchString(c.src) {
				t.Fatalf("%s no longer declares a single shared FlagEvent value (pattern: %s)", c.name, c.declare)
			}
			if !regexp.MustCompile(c.pubsub).MatchString(c.src) {
				t.Errorf("%s no longer publishes the shared event value via pub/sub (pattern: %s)", c.name, c.pubsub)
			}
			if !regexp.MustCompile(c.stream).MatchString(c.src) {
				t.Errorf("%s no longer publishes the shared event value via Streams (pattern: %s)", c.name, c.stream)
			}
		})
	}
}
