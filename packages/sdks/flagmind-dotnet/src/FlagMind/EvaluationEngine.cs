using Murmur;
using System.Text;

namespace Tombstone;

public class EvaluationEngine
{
    private static readonly MurmurHash _hasher = MurmurHash.Create32(seed: 0, managed: true);

    public EvaluationResult<T> Evaluate<T>(
        FlagEnvironmentState? flagState,
        EvaluationContext context,
        T defaultValue,
        string flagKey)
    {
        if (flagState is null)
            return new(defaultValue, EvaluationReason.Error, false, flagKey);

        if (!flagState.Enabled)
            return new(defaultValue, EvaluationReason.Off, true, flagKey);

        if (flagState.RolloutPct >= 100)
            return new(CastEnabled(defaultValue), EvaluationReason.Fallthrough, true, flagKey);

        if (flagState.RolloutPct <= 0)
            return new(defaultValue, EvaluationReason.Fallthrough, true, flagKey);

        return IsInRollout(flagKey, context.UserId, flagState.RolloutPct)
            ? new(CastEnabled(defaultValue), EvaluationReason.Fallthrough, true, flagKey)
            : new(defaultValue, EvaluationReason.Fallthrough, true, flagKey);
    }

    // MurmurHash3 unsigned 32-bit — matches TypeScript murmurhash.v3() >>> 0 % 100
    private static bool IsInRollout(string flagKey, string userId, int rolloutPct)
    {
        var bytes = Encoding.UTF8.GetBytes(flagKey + userId);
        var hash = _hasher.ComputeHash(bytes);
        uint bucket = BitConverter.ToUInt32(hash, 0) % 100;
        return bucket < (uint)rolloutPct;
    }

    private static T CastEnabled<T>(T defaultValue) =>
        defaultValue is bool ? (T)(object)true : defaultValue;
}
