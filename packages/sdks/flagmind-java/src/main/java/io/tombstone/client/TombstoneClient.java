package io.tombstone.client;

import com.fasterxml.jackson.databind.ObjectMapper;
import okhttp3.*;
import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.types.*;
import java.io.*;
import java.util.*;
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
        return engine.evaluate(state.orElse(null), Collections.emptyList(), context, def, flagKey);
    }

    public boolean isEnabled(String flagKey, EvaluationContext context) {
        EvaluationResult<Boolean> r = evaluate(flagKey, context);
        return Boolean.TRUE.equals(r.value());
    }

    public boolean isConnected() { return connected.get(); }
    public Set<String> flagKeys() { return cache.flagKeys(); }

    private void fetchSnapshot() throws IOException {
        Request req = new Request.Builder()
            .url(apiUrl + "/api/v1/environments/snapshot?environment=" + environment)
            .header("Authorization", "Bearer " + sdkKey)
            .get().build();
        try (Response resp = http.newCall(req).execute()) {
            if (!resp.isSuccessful() || resp.body() == null) return;
            Map<?, ?> data = mapper.readValue(resp.body().string(), Map.class);
            List<?> flags = (List<?>) data.get("flags");
            if (flags == null) return;
            List<FlagEnvironmentState> states = new ArrayList<>();
            for (Object f : flags) {
                Map<?, ?> fm = (Map<?, ?>) f;
                states.add(new FlagEnvironmentState(
                    str(fm, "flag_id"), str(fm, "flag_key"), str(fm, "environment"),
                    Boolean.TRUE.equals(fm.get("enabled")),
                    fm.get("rollout_pct") instanceof Number n ? n.intValue() : 0,
                    str(fm, "safe_default"), 0L
                ));
            }
            cache.loadSnapshot(states);
        }
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
                        while ((line = reader.readLine()) != null && connected.get()) {
                            if (line.startsWith("data:")) {
                                String json = line.substring(5).trim();
                                applyEvent(json);
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

    private static String str(Map<?, ?> m, String k) {
        Object v = m.get(k);
        return v != null ? v.toString() : "";
    }

    @Override
    public void close() {
        connected.set(false);
        if (sseThread != null) sseThread.interrupt();
    }
}
