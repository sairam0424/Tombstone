package io.tombstone.client;

import com.fasterxml.jackson.databind.ObjectMapper;
import okhttp3.*;
import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.types.*;
import java.io.*;
import java.util.*;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

public class TombstoneClient implements Closeable {
    private final String sdkKey;
    private final String environment;
    private final String apiUrl;
    private final String gatewayUrl;
    private final Map<String, Object> defaults;
    private final FlagCache cache;
    private final EvaluationEngine engine;
    private final OkHttpClient http;
    private final ObjectMapper mapper;
    private final AtomicBoolean connected;
    private Thread sseThread;

    // Debounced recovery from gateway "lag" events. A slow client can receive a
    // burst of lag frames back-to-back; coalesce them into a single snapshot
    // refetch via cancel+reschedule on a single-thread scheduler.
    private static final long LAG_REFETCH_DEBOUNCE_MS = 500L;
    private final ScheduledExecutorService lagScheduler;
    private ScheduledFuture<?> pendingLagRefetch;

    public TombstoneClient(String sdkKey, String environment, String apiUrl,
                          String gatewayUrl, Map<String, Object> defaults) {
        this.sdkKey = sdkKey;
        this.environment = environment;
        this.apiUrl = apiUrl != null ? apiUrl : "http://localhost:8081";
        this.gatewayUrl = gatewayUrl != null ? gatewayUrl : "http://localhost:8080";
        this.defaults = defaults != null ? defaults : Collections.emptyMap();
        this.cache = new FlagCache();
        this.engine = new EvaluationEngine();
        this.http = new OkHttpClient();
        this.mapper = new ObjectMapper();
        this.connected = new AtomicBoolean(false);
        this.lagScheduler = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "tombstone-lag-refetch");
            t.setDaemon(true);
            return t;
        });
    }

    public void connect() throws IOException {
        fetchSnapshot();
        startSseListener();
        connected.set(true);
    }

    @SuppressWarnings("unchecked")
    public <T> EvaluationResult<T> evaluate(String flagKey, EvaluationContext context) {
        Optional<FlagEnvironmentState> state = cache.get(flagKey);
        T def = (T) defaults.getOrDefault(flagKey, Boolean.FALSE);
        return engine.evaluate(state.orElse(null), context, def, flagKey);
    }

    public boolean isEnabled(String flagKey, EvaluationContext context) {
        EvaluationResult<Boolean> r = evaluate(flagKey, context);
        return Boolean.TRUE.equals(r.value());
    }

    public boolean isConnected() { return connected.get(); }
    public Set<String> flagKeys() { return cache.flagKeys(); }

    // Package-private (not private) so the SSE-recovery unit test can override it
    // to observe refetch calls without touching the network. connect(), reconnect,
    // and lag-recovery all funnel through this single snapshot-fetch path.
    void fetchSnapshot() throws IOException {
        Request req = new Request.Builder()
            .url(apiUrl + "/api/v1/environments/snapshot?environment=" + environment)
            .header("Authorization", "Bearer " + sdkKey)
            .get().build();
        try (Response resp = http.newCall(req).execute()) {
            if (!resp.isSuccessful() || resp.body() == null) return;
            cache.loadSnapshot(parseSnapshotResponse(resp.body().string()));
        }
    }

    // Package-private so a test can exercise the real wire-parsing logic
    // directly with a hand-built JSON string, without standing up a mock
    // HTTP server -- mirrors fetchSnapshot()'s own package-private
    // visibility, which exists for the identical reason (see its comment).
    List<FlagEnvironmentState> parseSnapshotResponse(String rawJson) throws IOException {
        Map<?, ?> data = mapper.readValue(rawJson, Map.class);
        List<?> flags = (List<?>) data.get("flags");
        if (flags == null) return List.of();
        List<FlagEnvironmentState> states = new ArrayList<>();
        for (Object f : flags) {
            Map<?, ?> fm = (Map<?, ?>) f;
            // FlagEnvironmentState.simple() hardcoded prerequisites/
            // targetingRules/targetList to List.of() and hashVersion to 1
            // regardless of what the wire actually sent -- the "simple"
            // factory is for hand-built test fixtures, not a real
            // snapshot response, but this was the ONLY place a real
            // FlagEnvironmentState was ever constructed from wire data,
            // so this client's own prerequisite gating never worked
            // against a real backend at all (found while investigating
            // SDK-4's prerequisites-streaming follow-up). flag-api's
            // real snapshot response has no targeting_rules/target_list/
            // hash_version fields today -- those stay empty/default 1,
            // same as before -- only prerequisites and updated_at
            // (also previously hardcoded to 0) are now read for real.
            states.add(new FlagEnvironmentState(
                str(fm, "flag_id"), str(fm, "flag_key"), str(fm, "environment"),
                Boolean.TRUE.equals(fm.get("enabled")),
                fm.get("rollout_pct") instanceof Number n ? n.intValue() : 0,
                str(fm, "safe_default"),
                fm.get("updated_at") instanceof Number n ? n.longValue() : 0L,
                parsePrerequisites(fm.get("prerequisites")),
                List.of(), List.of(), 1
            ));
        }
        return states;
    }

    // flag-api's real wire shape (services/flag-api/internal/api/v1/
    // environments.go's SnapshotPrerequisite): {"id", "flag_key",
    // "required_variation", "gate", "priority"} -- "flag_key", NOT
    // "prereq_flag_key" (that's only flag_prerequisites' own DB column
    // name, matching proto's ParentCondition message). "gate" defaults to
    // true (hard-blocking) when the wire omits it, matching flag-api's own
    // AddPrerequisite default.
    private static List<FlagPrerequisite> parsePrerequisites(Object raw) {
        if (!(raw instanceof List<?> rawList)) return List.of();
        List<FlagPrerequisite> result = new ArrayList<>(rawList.size());
        for (Object p : rawList) {
            if (!(p instanceof Map<?, ?> pm)) continue;
            result.add(new FlagPrerequisite(
                str(pm, "flag_key"),
                str(pm, "required_variation"),
                !Boolean.FALSE.equals(pm.get("gate"))
            ));
        }
        return result;
    }

    private void startSseListener() {
        sseThread = new Thread(() -> {
            while (connected.get()) {
                try {
                    Request req = new Request.Builder()
                        .url(gatewayUrl + "/api/v1/stream?environment=" + environment)
                        .header("Authorization", "Bearer " + sdkKey)
                        .header("Accept", "text/event-stream")
                        .get().build();
                    try (Response resp = http.newCall(req).execute()) {
                        if (resp.body() == null) continue;
                        BufferedReader reader = new BufferedReader(new InputStreamReader(resp.body().byteStream()));
                        String line;
                        String eventType = "message";
                        while ((line = reader.readLine()) != null && connected.get()) {
                            if (line.isEmpty()) {
                                eventType = "message"; // blank line ends the SSE frame — reset
                            } else if (line.startsWith("event:")) {
                                eventType = line.substring(6).trim();
                            } else if (line.startsWith("data:")) {
                                String json = line.substring(5).trim();
                                if ("lag".equals(eventType)) {
                                    // Gateway dropped a buffered flag update for this slow client
                                    // (its 64-slot buffer was full). Recover the lost update by
                                    // refetching the full snapshot, debounced to coalesce bursts.
                                    scheduleLagRefetch();
                                } else {
                                    applyEvent(json);
                                }
                            }
                        }
                    }
                } catch (Exception e) {
                    if (!connected.get()) break;
                    try { Thread.sleep(3000); } catch (InterruptedException ie) { Thread.currentThread().interrupt(); break; }
                }
            }
        }, "tombstone-sse");
        sseThread.setDaemon(true);
        sseThread.start();
    }

    private void applyEvent(String json) {
        try {
            Map<?, ?> m = mapper.readValue(json, Map.class);
            String flagKey = (String) m.get("flag_key");
            boolean enabled = Boolean.TRUE.equals(m.get("enabled"));
            int pct = m.get("rollout_pct") instanceof Number n ? n.intValue() : 0;
            long ts = m.get("ts") instanceof Number n ? n.longValue() : 0L;
            cache.applyEvent(flagKey, enabled, pct, ts);
        } catch (Exception ignored) {}
    }

    // Coalesce a burst of lag events into a single snapshot refetch: each lag
    // event cancels any pending refetch and reschedules one debounce window out.
    // synchronized to stay consistent with close(), which shuts the scheduler down.
    synchronized void scheduleLagRefetch() {
        if (lagScheduler.isShutdown()) return;
        if (pendingLagRefetch != null) pendingLagRefetch.cancel(false);
        pendingLagRefetch = lagScheduler.schedule(this::runLagRefetch,
            lagRefetchDebounceMillis(), TimeUnit.MILLISECONDS);
    }

    private void runLagRefetch() {
        try {
            fetchSnapshot();
        } catch (IOException e) {
            // Best-effort recovery: a failed refetch leaves the cache unchanged;
            // the next event or reconnect refreshes it. Mirrors applyEvent's swallow.
        }
    }

    // Overridable so tests can shrink the debounce window; 500ms in production.
    long lagRefetchDebounceMillis() { return LAG_REFETCH_DEBOUNCE_MS; }

    private static String str(Map<?, ?> m, String k) {
        Object v = m.get(k);
        return v != null ? v.toString() : "";
    }

    @Override
    public synchronized void close() {
        connected.set(false);
        if (sseThread != null) sseThread.interrupt();
        if (pendingLagRefetch != null) pendingLagRefetch.cancel(false);
        lagScheduler.shutdownNow();
    }
}
