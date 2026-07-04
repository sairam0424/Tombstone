# Tombstone v2 Code Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete all remaining code phases for Tombstone v2 — Slack HTTP routes, governance loop, Redis Streams migration, Go test coverage uplift, mTLS, and Argos LLM rule generation.

**Architecture:** Four independent phases executed in priority order: (1) Slack + Governance — wire existing stubs to real HTTP routes and scaffold governance domain loop; (2) Redis Streams — replace Redis pub/sub with XADD/XREADGROUP in flag-api and gateway; (3) Go coverage uplift — add table-driven tests across flag-api, gateway, evaluator to reach ≥80%; (4) mTLS — mutual TLS between internal services behind MTLS_ENABLED=true flag. Argos LLM is optional Phase 5 requiring ANTHROPIC_API_KEY.

**Tech Stack:** Go 1.25 (chi v5, go-redis/v9, zap, stdlib crypto/tls), Python 3.12 (FastAPI, httpx, Anthropic SDK), Docker Compose volumes, GitHub Actions

## Global Constraints

- `GOWORK=off` for all per-service Go builds and tests
- All commits use conventional format: `type(scope): description`
- All Go tests: `cd services/<svc> && GOWORK=off go test ./... -v`
- All Python tests: `cd services/intelligence && python -m pytest tests/ -v`
- Never break `make dev` — Docker Compose must stay functional after every task
- Base branch: `main`
- Never commit `infra/.env` or `~/.pypirc`

---

## Phase 1: Slack HTTP Routes + Governance Loop

**Source plan:** `~/.claude/plans/plan-slack-governance.md`
**Effort:** ~1 day | **Priority:** High (completes 70% built code)

### Task 1.1: Wire Slack slash command endpoint

**Files:**
- Create: `services/marketplace/internal/api/v1/slack.go`
- Modify: `services/marketplace/cmd/main.go` — add route registrations

**Interfaces:**
- Consumes: `integrations.SlackApp.VerifySignature()`, `HandleSlashCommand()`, `HandleBlockAction()`
- Produces: `GET|POST /api/v1/marketplace/slack/commands`, `POST /api/v1/marketplace/slack/actions`

- [ ] **Step 1: Read existing Handler struct**
```bash
cat services/marketplace/internal/api/v1/handler.go | head -40
```

- [ ] **Step 2: Create `services/marketplace/internal/api/v1/slack.go`**
```go
package v1

import (
    "encoding/json"
    "io"
    "net/http"
    "net/url"
    "os"

    "go.uber.org/zap"
    "github.com/sairam0424/Tombstone/services/marketplace/internal/integrations"
)

func (h *Handler) HandleSlackCommands(w http.ResponseWriter, r *http.Request) {
    rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
    if err != nil {
        h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
        return
    }
    timestamp := r.Header.Get("X-Slack-Request-Timestamp")
    signature := r.Header.Get("X-Slack-Signature")
    if secret := os.Getenv("SLACK_SIGNING_SECRET"); secret != "" {
        if !h.slackApp.VerifySignature(timestamp, string(rawBody), signature) {
            h.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
            return
        }
    }
    form, err := url.ParseQuery(string(rawBody))
    if err != nil {
        h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
        return
    }
    cmd := integrations.SlashCommand{
        Command:     form.Get("command"),
        Text:        form.Get("text"),
        UserID:      form.Get("user_id"),
        ChannelID:   form.Get("channel_id"),
        ResponseURL: form.Get("response_url"),
    }
    msg, err := h.slackApp.HandleSlashCommand(r.Context(), cmd)
    if err != nil {
        h.logger.Error("slack command handler failed", zap.Error(err))
        h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
        return
    }
    h.writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) HandleSlackActions(w http.ResponseWriter, r *http.Request) {
    if err := r.ParseForm(); err != nil {
        h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad form"})
        return
    }
    var action integrations.BlockAction
    if err := json.Unmarshal([]byte(r.FormValue("payload")), &action); err != nil {
        h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
        return
    }
    if err := h.slackApp.HandleBlockAction(r.Context(), action); err != nil {
        h.logger.Error("slack action handler failed", zap.Error(err))
        h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 3: Register routes in cmd/main.go**

Find the existing route block (search for `r.Route("/api/v1"`) and add:
```go
r.Post("/marketplace/slack/commands", h.HandleSlackCommands)
r.Post("/marketplace/slack/actions", h.HandleSlackActions)
```

- [ ] **Step 4: Build to verify no compile errors**
```bash
cd services/marketplace && GOWORK=off go build ./... 2>&1
```
Expected: no output (clean build)

- [ ] **Step 5: Write tests**

Create `services/marketplace/internal/api/v1/slack_test.go`:
```go
package v1_test

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestHandleSlackCommands_MissingSignature_Passes(t *testing.T) {
    // When SLACK_SIGNING_SECRET is not set, signature check is skipped
    body := "command=/tombstone&text=status+checkout-v2&user_id=U123&channel_id=C123&response_url=https://hooks.slack.com/x"
    req := httptest.NewRequest(http.MethodPost, "/slack/commands", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    // handler call — verify no 401
    _ = w // actual handler wiring in integration test
}
```

- [ ] **Step 6: Commit**
```bash
git add services/marketplace/internal/api/v1/slack.go services/marketplace/cmd/main.go
git commit -m "feat(marketplace): wire Slack slash command + action HTTP endpoints"
```

---

### Task 1.2: Scaffold Governance domain loop

**Files:**
- Create: `domains/governance/README.md`
- Modify: `scripts/loop-governance.sh` — add alert-sending on health < 0.80

**Interfaces:**
- Consumes: `GET /api/v1/flags/compliance` (flag-api endpoint) → returns `{"health_score": float, "stale_count": int}`
- Produces: Slack alert when `health_score < 0.80` or `stale_count > 50`

- [ ] **Step 1: Read existing loop-governance.sh**
```bash
cat scripts/loop-governance.sh
```

- [ ] **Step 2: Read existing domains/ structure**
```bash
ls domains/ && cat domains/flag-cleanup/README.md | head -30
```

- [ ] **Step 3: Add alert-sending logic to loop-governance.sh**

Find the section that checks health_score and add after it:
```bash
if (( $(echo "$HEALTH_SCORE < 0.80" | bc -l) )); then
  log "ALERT: health score $HEALTH_SCORE below 0.80 threshold"
  if [ -n "${SLACK_WEBHOOK_URL:-}" ]; then
    curl -s -X POST "$SLACK_WEBHOOK_URL" \
      -H "Content-Type: application/json" \
      -d "{\"text\":\"⚠️ Tombstone governance alert: health score is $HEALTH_SCORE (threshold: 0.80). Stale flags: $STALE_COUNT. Review: $TOMBSTONE_API_URL/governance\"}"
  fi
fi
```

- [ ] **Step 4: Create `domains/governance/README.md`**
```markdown
# Governance Domain

**Charter:** Monitor weekly flag health — detect SOC2 evidence gaps, stale flag sprawl, and health score degradation before they become incidents.

**Cadence:** Weekly, Monday 06:00 UTC

**Script:** `scripts/loop-governance.sh`

**Metrics:**
- `health_score` — composite score (0-1); alert threshold: 0.80
- `stale_count` — flags at 100% rollout for 30+ days; alert threshold: 50

**Activation:** Set `TOMBSTONE_API_URL`, `TOMBSTONE_INTELLIGENCE_URL`, `SLACK_WEBHOOK_URL` as GitHub Actions repo variables.

## Timeline
<!-- loop-governance appends entries here after each run -->
```

- [ ] **Step 5: Commit**
```bash
git add domains/governance/README.md scripts/loop-governance.sh
git commit -m "feat(governance): scaffold governance domain loop with Slack alerts"
```

---

## Phase 2: Redis Streams Migration

**Source plan:** `~/.claude/plans/plan-redis-streams.md`
**Effort:** ~2 days | **Priority:** High (removes Kafka dependency, simplifies stack)

### Task 2.1: Add XADD alongside PUBLISH in flag-api

**Files:**
- Modify: `services/flag-api/internal/api/v1/flags.go`
- Modify: `services/flag-api/internal/scheduler/scheduler.go`

**Interfaces:**
- Consumes: `h.rdb *redis.Client` (already on FlagHandler)
- Produces: Redis stream entries at `tombstone:stream:{environment}`

- [ ] **Step 1: Read current publishEvent() in flags.go**
```bash
grep -n "publishEvent\|rdb.Publish\|PUBLISH" services/flag-api/internal/api/v1/flags.go | head -20
```

- [ ] **Step 2: Add publishToStream() method after publishEvent() in flags.go**
```go
// publishToStream writes the event to the Redis Stream alongside the legacy PUBLISH.
// Stream key: tombstone:stream:{environment}. Removed in v2.1 when PUBLISH is dropped.
func (h *FlagHandler) publishToStream(ctx context.Context, environment string, event FlagEvent) {
    payload, err := json.Marshal(event)
    if err != nil {
        return
    }
    streamKey := fmt.Sprintf("tombstone:stream:%s", environment)
    if err := h.rdb.XAdd(ctx, &redis.XAddArgs{
        Stream: streamKey,
        MaxLen: 10000,
        Approx: true,
        Values: map[string]interface{}{
            "event":       event.Reason,
            "flag_key":    event.FlagKey,
            "environment": environment,
            "payload":     string(payload),
        },
    }).Err(); err != nil {
        h.logger.Warn("redis xadd failed", zap.Error(err), zap.String("stream", streamKey))
    }
}
```

- [ ] **Step 3: Call publishToStream() wherever publishEvent() is called**

After every `h.publishEvent(ctx, env, event)` call, add:
```go
h.publishToStream(ctx, env, event)
```

- [ ] **Step 4: Build**
```bash
cd services/flag-api && GOWORK=off go build ./...
```

- [ ] **Step 5: Write test for publishToStream**
```go
func TestPublishToStream_WritesCorrectFields(t *testing.T) {
    // Uses miniredis for Redis mock
    // Verify XLEN("tombstone:stream:development") == 1 after publishToStream()
    // Verify fields: event, flag_key, environment, payload all present
}
```

- [ ] **Step 6: Commit**
```bash
git add services/flag-api/internal/api/v1/flags.go services/flag-api/internal/scheduler/scheduler.go
git commit -m "feat(flag-api): publish events to Redis Streams alongside legacy PUBLISH"
```

---

### Task 2.2: Switch gateway broadcaster to XREADGROUP

**Files:**
- Modify: `services/gateway/internal/hub/broadcaster.go`

**Interfaces:**
- Consumes: `tombstone:stream:{environment}` Redis stream
- Consumes: consumer group `gateway-workers`, consumer `gateway-{hostname}`
- Produces: calls `hub.Broadcast(env, eventJSON)` — same as before

- [ ] **Step 1: Read current broadcaster.go**
```bash
cat services/gateway/internal/hub/broadcaster.go
```

- [ ] **Step 2: Add createConsumerGroup helper**
```go
func ensureConsumerGroup(ctx context.Context, rdb *redis.Client, stream, group string) error {
    err := rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
    if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
        return err
    }
    return nil
}
```

- [ ] **Step 3: Replace PSubscribe loop with XREADGROUP loop**
```go
func (b *Broadcaster) RunStreams(ctx context.Context, environment string) {
    streamKey := fmt.Sprintf("tombstone:stream:%s", environment)
    consumerName := fmt.Sprintf("gateway-%s", hostname())
    const group = "gateway-workers"

    if err := ensureConsumerGroup(ctx, b.rdb, streamKey, group); err != nil {
        b.logger.Error("failed to create consumer group", zap.Error(err))
        return
    }

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }
        msgs, err := b.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
            Group:    group,
            Consumer: consumerName,
            Streams:  []string{streamKey, ">"},
            Count:    10,
            Block:    time.Second,
        }).Result()
        if err != nil && err != redis.Nil {
            b.logger.Warn("xreadgroup error", zap.Error(err))
            time.Sleep(100 * time.Millisecond)
            continue
        }
        for _, stream := range msgs {
            for _, msg := range stream.Messages {
                payload, _ := msg.Values["payload"].(string)
                b.hub.Broadcast(environment, []byte(payload))
                b.rdb.XAck(ctx, streamKey, group, msg.ID)
            }
        }
    }
}
```

- [ ] **Step 4: Build**
```bash
cd services/gateway && GOWORK=off go build ./...
```

- [ ] **Step 5: Commit**
```bash
git add services/gateway/internal/hub/broadcaster.go
git commit -m "feat(gateway): switch broadcaster to Redis Streams XREADGROUP consumer group"
```

---

### Task 2.3: Update docker-compose.yml + README

**Files:**
- Modify: `infra/docker-compose.yml` — add CONSUMER_BACKEND: redis to gateway service
- Modify: `README.md` — note Kafka is now optional

- [ ] **Step 1: Add env var to gateway in docker-compose.yml**
```yaml
# Under gateway service environment:
CONSUMER_BACKEND: streams
```

- [ ] **Step 2: Add note to README What Runs table**

Under the Kafka row, add: `(optional — only needed if CONSUMER_BACKEND=kafka)`

- [ ] **Step 3: Verify make dev still works**
```bash
docker compose -f infra/docker-compose.yml config 2>&1 | grep -i error
```
Expected: no errors

- [ ] **Step 4: Commit**
```bash
git add infra/docker-compose.yml README.md
git commit -m "chore(infra): mark Kafka as optional, gateway defaults to Redis Streams"
```

---

## Phase 3: Go Test Coverage Uplift

**Goal:** Reach ≥80% coverage on non-data packages in flag-api, gateway, evaluator for awesome-go eligibility.
**Effort:** ~2 days

### Task 3.1: flag-api — table-driven tests for core handlers

**Files:**
- Modify: `services/flag-api/internal/api/v1/flags_test.go` (or create if missing)

- [ ] **Step 1: Check existing test coverage**
```bash
cd services/flag-api && GOWORK=off go test ./... -coverprofile=coverage.out -covermode=atomic && go tool cover -func=coverage.out | grep -v "100.0%" | tail -20
```

- [ ] **Step 2: Add table-driven tests for CreateFlag handler**
```go
func TestCreateFlag_Validation(t *testing.T) {
    cases := []struct {
        name       string
        body       string
        wantStatus int
    }{
        {"valid boolean flag", `{"key":"test-flag","flag_type":"BOOLEAN","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`, 201},
        {"missing key", `{"flag_type":"BOOLEAN","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`, 400},
        {"invalid flag_type", `{"key":"test","flag_type":"INVALID","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`, 400},
        {"empty body", `{}`, 400},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", strings.NewReader(tc.body))
            req.Header.Set("Content-Type", "application/json")
            w := httptest.NewRecorder()
            // Call handler with test DB
            if w.Code != tc.wantStatus {
                t.Errorf("want %d got %d", tc.wantStatus, w.Code)
            }
        })
    }
}
```

- [ ] **Step 3: Add tests for audit log Merkle chain**
```go
func TestAuditLog_MerkleChain(t *testing.T) {
    // Verify sha256(id|event_type|actor|prev_state|new_state|ts) matches stored hash
    // Verify prev_hash of entry N equals hash of entry N-1
}
```

- [ ] **Step 4: Add tests for circuit breaker state machine**
```go
func TestCircuitBreaker_StateTransitions(t *testing.T) {
    cases := []struct {
        name       string
        errorRate  float64
        requests   int64
        wantState  string
    }{
        {"below threshold stays closed", 0.03, 100, "CLOSED"},
        {"above threshold opens", 0.06, 100, "OPEN"},
        {"insufficient requests stays closed", 0.10, 50, "CLOSED"},
    }
    // ...
}
```

- [ ] **Step 5: Re-run coverage and verify improvement**
```bash
cd services/flag-api && GOWORK=off go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | grep "total:"
```
Target: total coverage ≥ 60% (incremental — full 80% across all tasks)

- [ ] **Step 6: Commit**
```bash
git add services/flag-api/internal/api/v1/
git commit -m "test(flag-api): add table-driven tests for CreateFlag, audit Merkle chain, circuit breaker"
```

---

### Task 3.2: evaluator — blast radius + rollback tests

**Files:**
- Modify: `services/evaluator/internal/blast/blast_radius_test.go`
- Modify: `services/evaluator/internal/rollback/rollback_test.go`

- [ ] **Step 1: Check current evaluator coverage**
```bash
cd services/evaluator && GOWORK=off go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | grep "total:"
```

- [ ] **Step 2: Add blast radius tier classification tests**
```go
func TestBlastRadiusTier(t *testing.T) {
    cases := []struct {
        trafficPct   float64
        depCount     int
        errorDelta   float64
        wantTier     string
    }{
        {0.60, 3, 0.06, "BLOCKED"},
        {0.30, 2, 0.03, "HIGH"},
        {0.15, 1, 0.01, "MEDIUM"},
        {0.05, 0, 0.00, "LOW"},
    }
    for _, tc := range cases {
        t.Run(tc.wantTier, func(t *testing.T) {
            tier := classifyBlastRadius(tc.trafficPct, tc.depCount, tc.errorDelta)
            if tier != tc.wantTier {
                t.Errorf("want %s got %s", tc.wantTier, tier)
            }
        })
    }
}
```

- [ ] **Step 3: Add rollback execution tests**
```go
func TestRollback_DisablesFlagOnTrip(t *testing.T) {
    // Mock flag-api HTTP endpoint
    // Verify PATCH /api/v1/flags/{key}/environments/{env} called with {"enabled": false}
    // Verify audit entry written
}
```

- [ ] **Step 4: Commit**
```bash
git add services/evaluator/
git commit -m "test(evaluator): add blast radius tier + rollback execution tests"
```

---

### Task 3.3: gateway — SSE hub + broadcaster tests

**Files:**
- Create/Modify: `services/gateway/internal/hub/hub_test.go`

- [ ] **Step 1: Add SSE connection pool tests**
```go
func TestHub_BroadcastToMultipleClients(t *testing.T) {
    h := NewHub(zap.NewNop())
    var wg sync.WaitGroup
    received := make([][]byte, 3)
    for i := 0; i < 3; i++ {
        ch := make(chan []byte, 1)
        h.Subscribe("production", ch)
        idx := i
        wg.Add(1)
        go func() {
            defer wg.Done()
            received[idx] = <-ch
        }()
    }
    h.Broadcast("production", []byte(`{"flag_key":"test"}`))
    wg.Wait()
    for i, msg := range received {
        if string(msg) != `{"flag_key":"test"}` {
            t.Errorf("client %d: got %s", i, msg)
        }
    }
}
```

- [ ] **Step 2: Add backpressure lag event test**
```go
func TestHub_LagEventOnFullBuffer(t *testing.T) {
    // Fill client channel buffer to capacity
    // Verify next Broadcast sends "lag" event instead of dropping silently
}
```

- [ ] **Step 3: Commit**
```bash
git add services/gateway/internal/hub/
git commit -m "test(gateway): add SSE hub broadcast + backpressure lag tests"
```

---

## Phase 4: mTLS Between Internal Services

**Source plan:** `~/.claude/plans/plan-mtls.md`
**Effort:** ~3 days | **Priority:** Medium (security hardening, opt-in)

### Task 4.1: Verify existing tlsutil package

**Files:**
- Read: `services/flag-api/internal/tlsutil/certgen.go`
- Read: `services/flag-api/internal/tlsutil/loader.go`

- [ ] **Step 1: Check if tlsutil already exists**
```bash
ls services/flag-api/internal/tlsutil/ && cat services/flag-api/internal/tlsutil/certgen.go | head -40
```

- [ ] **Step 2: If certgen.go exists — test it**
```bash
cd services/flag-api && GOWORK=off go test ./internal/tlsutil/... -v
```
Expected: PASS

- [ ] **Step 3: If certgen.go missing — create it**
```go
package tlsutil

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "encoding/pem"
    "math/big"
    "os"
    "path/filepath"
    "time"
)

func GenerateCACert() (*tls.Certificate, *x509.CertPool, error) {
    caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    if err != nil {
        return nil, nil, err
    }
    caTemplate := &x509.Certificate{
        SerialNumber:          big.NewInt(1),
        Subject:               pkix.Name{CommonName: "tombstone-ca"},
        NotBefore:             time.Now(),
        NotAfter:              time.Now().Add(365 * 24 * time.Hour),
        IsCA:                  true,
        KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
        BasicConstraintsValid: true,
    }
    caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
    if err != nil {
        return nil, nil, err
    }
    caCert, err := x509.ParseCertificate(caDER)
    if err != nil {
        return nil, nil, err
    }
    pool := x509.NewCertPool()
    pool.AddCert(caCert)
    tlsCert := tls.Certificate{Certificate: [][]byte{caDER}, PrivateKey: caKey, Leaf: caCert}
    return &tlsCert, pool, nil
}
```

- [ ] **Step 4: Build**
```bash
cd services/flag-api && GOWORK=off go build ./internal/tlsutil/...
```

- [ ] **Step 5: Commit**
```bash
git add services/flag-api/internal/tlsutil/
git commit -m "feat(flag-api): add tlsutil package for mTLS cert generation"
```

---

### Task 4.2: Wire mTLS to flag-api server startup

**Files:**
- Modify: `services/flag-api/cmd/main.go` — add TLS server config when MTLS_ENABLED=true

- [ ] **Step 1: Read current server startup in main.go**
```bash
grep -n "http.Server\|ListenAndServe\|MTLS" services/flag-api/cmd/main.go | head -20
```

- [ ] **Step 2: Add mTLS conditional to server startup**
```go
if os.Getenv("MTLS_ENABLED") == "true" {
    certsDir := os.Getenv("CERTS_DIR")
    if certsDir == "" {
        certsDir = "/certs"
    }
    tlsConf, err := tlsutil.LoadServerTLSConfig(certsDir)
    if err != nil {
        logger.Fatal("failed to load mTLS certs", zap.Error(err))
    }
    srv.TLSConfig = tlsConf
    logger.Info("mTLS enabled", zap.String("certs_dir", certsDir))
    logger.Fatal("server error", zap.Error(srv.ListenAndServeTLS("", "")))
} else {
    logger.Fatal("server error", zap.Error(srv.ListenAndServe()))
}
```

- [ ] **Step 3: Build**
```bash
cd services/flag-api && GOWORK=off go build ./...
```

- [ ] **Step 4: Commit**
```bash
git add services/flag-api/cmd/main.go
git commit -m "feat(flag-api): wire mTLS server config behind MTLS_ENABLED=true"
```

---

### Task 4.3: Add client cert to evaluator + gateway

**Files:**
- Modify: `services/evaluator/cmd/main.go` — add TLS client when MTLS_ENABLED=true
- Modify: `services/gateway/cmd/main.go` — same

- [ ] **Step 1: Add mTLS HTTP client to evaluator**
```go
var httpClient *http.Client
if os.Getenv("MTLS_ENABLED") == "true" {
    tlsConf, err := tlsutil.LoadClientTLSConfig(os.Getenv("CERTS_DIR"))
    if err != nil {
        logger.Fatal("failed to load client mTLS certs", zap.Error(err))
    }
    httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf}}
} else {
    httpClient = http.DefaultClient
}
```

- [ ] **Step 2: Same for gateway**

- [ ] **Step 3: Add certs volume to docker-compose.yml**
```yaml
volumes:
  certs:

# Under flag-api service:
volumes:
  - certs:/certs

# Under evaluator and gateway:
volumes:
  - certs:/certs:ro
```

- [ ] **Step 4: Build both services**
```bash
cd services/evaluator && GOWORK=off go build ./... && cd ../gateway && GOWORK=off go build ./...
```

- [ ] **Step 5: Commit**
```bash
git add services/evaluator/cmd/main.go services/gateway/cmd/main.go infra/docker-compose.yml
git commit -m "feat(mtls): add mTLS client cert to evaluator and gateway, wire certs volume"
```

---

## Phase 5: Argos LLM Rule Generation (optional — needs ANTHROPIC_API_KEY)

**Source plan:** `~/.claude/plans/plan-argos-llm.md`
**Effort:** ~1.5 days | **Priority:** Low (experimental)

### Task 5.1: Create rule_generator.py

**Files:**
- Create: `services/intelligence/app/anomaly/rule_generator.py`

- [ ] **Step 1: Check if file already exists**
```bash
ls services/intelligence/app/anomaly/
```

- [ ] **Step 2: If missing — create the 3-agent pipeline skeleton**
```python
"""
Argos 3-agent LLM pipeline for autonomous anomaly rule generation.
Requires ANTHROPIC_API_KEY. Returns graceful no-op when absent.
"""
import ast
import os
from dataclasses import dataclass
from typing import Optional
import httpx

@dataclass
class RuleResult:
    flag_key: str
    rule_code: str
    precision: float
    recall: float
    status: str  # "pending-approval" | "failed"
    error: Optional[str] = None

async def generate_rule(flag_key: str, metrics: dict, signals_dir: str) -> RuleResult:
    api_key = os.getenv("ANTHROPIC_API_KEY")
    if not api_key:
        return RuleResult(flag_key=flag_key, rule_code="", precision=0, recall=0,
                          status="failed", error="ANTHROPIC_API_KEY not set")

    # Agent 1: Detection — describe the anomaly pattern
    detection = await _detection_agent(flag_key, metrics, api_key)

    # Agent 2: Repair — generate and syntax-validate Python rule
    rule_code = await _repair_agent(flag_key, detection, api_key)
    try:
        ast.parse(rule_code)
    except SyntaxError as e:
        return RuleResult(flag_key=flag_key, rule_code=rule_code, precision=0, recall=0,
                          status="failed", error=f"syntax error: {e}")

    # Agent 3: Review — validate against held-out 20% of history
    precision, recall = await _review_agent(flag_key, rule_code, metrics, api_key)

    # Write to signals/ as pending-approval
    _write_signal(flag_key, rule_code, precision, recall, signals_dir)

    return RuleResult(flag_key=flag_key, rule_code=rule_code,
                      precision=precision, recall=recall, status="pending-approval")
```

- [ ] **Step 3: Wire endpoint in main.py**
```python
@app.post("/api/v1/intelligence/generate-rule")
async def generate_rule_endpoint(flag_key: str, db=Depends(get_db)):
    if not os.getenv("ANTHROPIC_API_KEY"):
        raise HTTPException(503, "Argos rule generation requires ANTHROPIC_API_KEY")
    result = await generate_rule(flag_key, get_metrics(flag_key), SIGNALS_DIR)
    return result
```

- [ ] **Step 4: Write test**
```python
def test_generate_rule_no_api_key(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    result = asyncio.run(generate_rule("test-flag", {}, "/tmp/signals"))
    assert result.status == "failed"
    assert "ANTHROPIC_API_KEY" in result.error
```

- [ ] **Step 5: Run test**
```bash
cd services/intelligence && python -m pytest tests/ -v -k "test_generate_rule_no_api_key"
```
Expected: PASS

- [ ] **Step 6: Commit**
```bash
git add services/intelligence/app/anomaly/rule_generator.py
git commit -m "feat(intelligence): add Argos 3-agent LLM rule generation pipeline (requires ANTHROPIC_API_KEY)"
```

---

## Execution Notes

- **Run phases in order:** 1 → 2 → 3 → 4 → 5
- Phase 3 (coverage) can run in parallel with Phase 2 on separate services
- Phase 5 is truly optional — skip entirely if no ANTHROPIC_API_KEY
- After every phase: `git push origin main` and verify CI is green
- Use `bash scripts/dev-local.sh status` to verify Docker stack is healthy after infra changes
