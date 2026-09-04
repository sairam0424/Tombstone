package io.tombstone.client;

import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;

/** Verifies SSE recovery from a gateway "lag" event. The gateway emits an
 *  {@code event: lag} frame right before it drops a buffered flag update for a
 *  slow client; the client must recover the lost update by refetching the full
 *  snapshot (the same {@link TombstoneClient#fetchSnapshot()} path connect()
 *  uses), debounced so a burst of lag frames collapses into a single refetch.
 *
 *  The transport is not exercised: a subclass overrides fetchSnapshot() to count
 *  invocations instead of hitting HTTP — the same "hand-built state, no network"
 *  approach the SDK's evaluation tests use. */
public class TombstoneClientLagTest {

    /** Counts refetches without touching the network and shrinks the debounce
     *  window so the test runs fast and deterministically. */
    static final class CountingClient extends TombstoneClient {
        final AtomicInteger fetches = new AtomicInteger();
        private final long debounceMs;
        private volatile CountDownLatch firstFetch = new CountDownLatch(1);

        CountingClient(long debounceMs) {
            super("test-key", "test", "http://api.invalid", "http://gw.invalid", Map.of());
            this.debounceMs = debounceMs;
        }

        @Override
        void fetchSnapshot() {
            fetches.incrementAndGet();
            firstFetch.countDown();
        }

        @Override
        long lagRefetchDebounceMillis() { return debounceMs; }

        boolean awaitFirstFetch(long ms) throws InterruptedException {
            return firstFetch.await(ms, TimeUnit.MILLISECONDS);
        }
    }

    @Test
    void singleLagEventTriggersExactlyOneRefetch() throws Exception {
        CountingClient client = new CountingClient(50);
        try {
            client.scheduleLagRefetch();

            assertTrue(client.awaitFirstFetch(2000),
                "a lag event should trigger a debounced snapshot refetch");
            // Wait well past the debounce window to catch any spurious extra refetch.
            Thread.sleep(200);
            assertEquals(1, client.fetches.get(),
                "a single lag event must trigger exactly one refetch");
        } finally {
            client.close();
        }
    }

    @Test
    void burstOfLagEventsCoalescesIntoOneRefetch() throws Exception {
        CountingClient client = new CountingClient(150);
        try {
            // Fire several lag events inside the debounce window. The tight loop
            // completes in microseconds — far under 150ms — so each event cancels
            // and reschedules the pending refetch, leaving exactly one to fire.
            for (int i = 0; i < 6; i++) {
                client.scheduleLagRefetch();
            }

            assertTrue(client.awaitFirstFetch(2000),
                "the coalesced refetch should fire once");
            // Wait past the debounce window again so any un-coalesced extras surface.
            Thread.sleep(300);
            assertEquals(1, client.fetches.get(),
                "a burst of lag events within the debounce window must trigger only one refetch");
        } finally {
            client.close();
        }
    }
}
