namespace Tombstone.Tests;
using Xunit;

/// <summary>
/// Regression test for the "lost update" race disclosed at the top of
/// FlagCache.cs: a plain read-modify-write let two concurrent mutations
/// both read the same `_state`, and whichever write landed second silently
/// discarded the OTHER's entire state, not just the key it touched. Seeds
/// N distinct flags, then fires N threads that each call ApplyEvent for
/// its OWN distinct flag key all at once (a Barrier forces every thread to
/// start its read-modify-write in the same instant, maximizing real
/// contention on the shared field rather than hoping for it). With the old
/// plain read/write code this reliably lost several of the N updates every
/// run; with the CompareExchange retry loop in UpdateState, every single
/// update must survive regardless of interleaving -- a hard correctness
/// guarantee, not a probabilistic one, mirroring the Java SDK's equivalent
/// regression test (FlagCacheTest.concurrentApplyEventsOnDistinctKeysNeverLoseAnUpdate).
/// </summary>
public class FlagCacheConcurrencyTests
{
    [Fact]
    public void ConcurrentApplyEventsOnDistinctKeysNeverLoseAnUpdate()
    {
        var cache = new FlagCache();
        const int n = 200;
        var seeded = new List<FlagEnvironmentState>();
        for (int i = 0; i < n; i++)
        {
            seeded.Add(new FlagEnvironmentState(
                $"id-{i}", $"flag-{i}", "prod", false, 0, "false", 0L,
                new(), new()));
        }
        cache.LoadSnapshot(seeded);

        // The barrier's party count (n) must equal the number of threads:
        // a Barrier only releases once EVERY party has signaled, so fewer
        // threads than parties would deadlock forever.
        using var barrier = new Barrier(n);
        var threads = new Thread[n];
        for (int i = 0; i < n; i++)
        {
            int idx = i;
            threads[i] = new Thread(() =>
            {
                barrier.SignalAndWait();
                cache.ApplyEvent($"flag-{idx}", true, 100, 999L);
            });
        }
        foreach (var t in threads) t.Start();
        foreach (var t in threads) Assert.True(t.Join(TimeSpan.FromSeconds(30)), "all ApplyEvent calls must finish");

        for (int i = 0; i < n; i++)
        {
            var key = $"flag-{i}";
            var flag = cache.Get(key);
            Assert.NotNull(flag);
            Assert.True(flag!.Enabled, $"{key}'s concurrent ApplyEvent was lost -- Enabled still false");
            Assert.Equal(100, flag.RolloutPct);
        }
    }
}
