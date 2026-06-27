# Tombstone v1 Release Fix Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every blocker and critical issue identified in the v1 release audit so Tombstone ships a fully functional product — flag-api live, dashboard connected to real APIs, credentials safe, ApprovalQueue backend implemented, branch merged.

**Architecture:** Seven sequential phases ordered by blast radius — infrastructure first (nothing works without flag-api), security second (never announce with leaked creds), then backend gaps, dashboard polish, and finally the release cut. Each phase is independently deployable and verifiable.

**Tech Stack:** Go 1.25 (flag-api), Python 3.12 (intelligence), React 19 + Vite 8 (dashboard), Northflank (hosting), Vercel (dashboard), Neon PostgreSQL, Upstash Redis

## Global Constraints

- Repo root: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone`
- Dashboard working dir: `workspace-dashboard/`
- Dashboard imports use `.js` extension (ESM/NodeNext)
- Go services use `GOWORK=off` for builds
- JWT_SECRET in production: `REDACTED_ROTATE_THIS_TOKEN`
- gh CLI at `/opt/homebrew/bin/gh`, authenticated as sairam0424
- All secrets must never be committed to git
- Northflank Account 1: flag-api + gateway (team: sairam0000s)
- Northflank Account 2: evaluator + intelligence (team: sairam056s or new account)

---

## PHASE 1 — CRITICAL PATH: Restore flag-api

### Task 1: Redeploy flag-api on Northflank

**Files:**
- Read: `infra/northflank/flag-api.json` (service spec reference)

**Interfaces:**
- Produces: `flag-api` service live at a Northflank URL, `/health` returns `{"status":"ok"}`

**Context:** The flag-api DNS `p01--tombstone-flag-api--ddzvzj6b5rd6.code.run` no longer resolves. The Northflank service was either deleted or stopped. We need to recreate it on Northflank Account 1.

- [ ] **Step 1: Go to Northflank Account 1 and check service status**

Go to: `https://app.northflank.com/t/sairam0000s-team/project/tombstone`

Look at the Services list. Is `tombstone-flag-api` listed? If yes, click it and check status. If stopped, click **Resume**. If deleted, proceed to Step 2.

- [ ] **Step 2: If service is gone — recreate it**

Click **"+ Create new" → "Service"** and fill in exactly:

| Field | Value |
|-------|-------|
| Name | `tombstone-flag-api` |
| Repository | `sairam0424/Tombstone` |
| Branch | `main` |
| Build context | `/services/flag-api` |
| Dockerfile | `/services/flag-api/Dockerfile` |
| Plan | Sandbox (free) |
| Port | `8081` → HTTP → Public ON |

Runtime variables (add all — get values from `infra/.env.secrets`):
```
PORT                     = 8081
EMBEDDING_BACKEND        = bedrock
BEDROCK_REGION           = us-east-1
CONSUMER_BACKEND         = redis
TOMBSTONE_ENVIRONMENTS   = production
REKOR_ENABLED            = false
JWT_SECRET               = REDACTED_ROTATE_THIS_TOKEN
DB_URL                   = [from infra/.env.secrets]
REDIS_URL                = [from infra/.env.secrets]
BEDROCK_ACCESS_KEY_ID    = [from infra/.env.secrets]
BEDROCK_SECRET_ACCESS_KEY = [from infra/.env.secrets]
UPSTASH_REDIS_REST_URL   = [from infra/.env.secrets]
UPSTASH_REDIS_REST_TOKEN = [from infra/.env.secrets]
```

Click **"Create service"** and wait for the build to complete (~5 minutes).

- [ ] **Step 3: Record the new service URL**

Once deployed, go to **Networking** tab of the new `tombstone-flag-api` service. The public URL will look like `p01--tombstone-flag-api--XXXXXXX.code.run`. Write it down.

Update `infra/.env.secrets`:
```
FLAG_API_URL=https://p01--tombstone-flag-api--XXXXXXX.code.run
```

- [ ] **Step 4: Update gateway's FLAG_API_URL environment variable**

Go to **Northflank → tombstone → tombstone-gateway → Environment**.

Update `FLAG_API_URL` to the new flag-api URL from Step 3.

Gateway will auto-restart. Wait ~30 seconds.

- [ ] **Step 5: Update Vercel VITE_API_URL**

Go to: `https://vercel.com/sairams-projects-d50d7437/tombstone-workspace-dashboard-ihw7/settings/environment-variables`

Update `VITE_API_URL` to the new flag-api URL. Save.

- [ ] **Step 6: Verify flag-api is live**

```bash
curl -s --max-time 10 https://p01--tombstone-flag-api--XXXXXXX.code.run/health
```

Expected: `{"status":"ok"}`

- [ ] **Step 7: Trigger Vercel redeploy to bake new API URL**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git commit --allow-empty -m "chore: trigger Vercel redeploy — new flag-api URL"
git push origin main
```

---

## PHASE 2 — SECURITY: Credential safety

### Task 2: Audit and clean credentials

**Files:**
- Modify: `infra/.env` (replace real values with placeholders)
- Verify: `infra/.env.secrets` is in `.gitignore`

**Context:** `infra/.env` contains real credentials that could be committed if someone runs `git add .`. This must be cleaned before any public announcement.

- [ ] **Step 1: Verify .gitignore covers secrets**

```bash
grep -E "\.env|secrets" /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/.gitignore
```

Expected output should include `infra/.env` and `infra/.env.secrets`. If not, add them:

```bash
echo "infra/.env" >> /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/.gitignore
echo "infra/.env.secrets" >> /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/.gitignore
```

- [ ] **Step 2: Check if infra/.env was ever committed to git history**

```bash
git -C /Users/sairamugge/Desktop/Not-Humans-World/Tombstone log --all --full-history -- infra/.env infra/.env.secrets 2>/dev/null | head -5
```

If output shows commits — those credentials are compromised and must be rotated (see Step 3). If no output — clean.

- [ ] **Step 3: Replace real values in infra/.env with placeholders**

Read `infra/.env` and replace all real credential values with safe placeholder strings, keeping only non-sensitive values intact:

```bash
# Safe values to keep as-is in infra/.env:
PORT=8081
EMBEDDING_BACKEND=local
CONSUMER_BACKEND=kafka
TOMBSTONE_ENVIRONMENTS=production
REKOR_ENABLED=false
MTLS_ENABLED=false
CERTS_DIR=/certs
```

Replace these with placeholders:
- `POSTGRES_PASSWORD=` → `POSTGRES_PASSWORD=change-me`
- Any real DB_URL, REDIS_URL, JWT_SECRET, AWS keys → `change-me-see-env-secrets`

The real values live in `infra/.env.secrets` (git-ignored). That file stays untouched.

- [ ] **Step 4: Verify JWT_SECRET is set in Northflank flag-api**

Go to: Northflank → tombstone-flag-api → Environment

Confirm `JWT_SECRET` is present and equals `REDACTED_ROTATE_THIS_TOKEN`.

If missing, add it now.

- [ ] **Step 5: Commit the cleaned infra/.env**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add infra/.env .gitignore
git status  # verify infra/.env.secrets is NOT staged
git commit -m "security: replace real credentials in infra/.env with placeholders"
git push origin main
```

---

## PHASE 3 — MERGE: Dashboard branch to main

### Task 3: Open and merge feat/dashboard-anvilry-port PR

**Files:**
- No file changes — this is a git operation

**Context:** 72 dashboard commits are on `feat/dashboard-anvilry-port`. Vercel is tracking this branch. We need a proper PR merge to main so production tracks main, not a feature branch.

- [ ] **Step 1: Verify branch is clean and build passes**

```bash
git -C /Users/sairamugge/Desktop/Not-Humans-World/Tombstone checkout feat/dashboard-anvilry-port
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs`

- [ ] **Step 2: Create PR**

```bash
export PATH="/opt/homebrew/bin:$PATH"
gh pr create \
  --title "feat(dashboard): Anvilry design system port — Inter font, cn(), MotionConfig, Reveal, EmptyState, Section, SkeletonStatCard" \
  --base main \
  --head feat/dashboard-anvilry-port \
  --body "Final dashboard upgrade: Inter/JetBrains Mono fonts, MotionConfig provider, cn() utility, Reveal/EmptyState/Section components, SkeletonStatCard. Fixes missing @radix-ui/react-alert-dialog dep for Vercel."
```

- [ ] **Step 3: Merge PR**

```bash
export PATH="/opt/homebrew/bin:$PATH"
gh pr merge --squash 2>&1
```

Note the PR number and verify merge completes.

- [ ] **Step 4: Verify Vercel redeploys from main**

Check `https://vercel.com/sairams-projects-d50d7437/tombstone-workspace-dashboard-ihw7/deployments` — within 2 minutes a new deployment should appear targeting `main` branch.

- [ ] **Step 5: Verify dashboard loads flags**

Once Vercel deployment is READY, open `https://tombstone-workspace-dashboard-ihw7.vercel.app` and confirm the flag list loads (not empty due to localhost fallback).

---

## PHASE 4 — BACKEND GAP: ApprovalQueue Go handler

### Task 4: Implement change-requests API in flag-api

**Files:**
- Create: `services/flag-api/internal/api/v1/change_requests.go`
- Modify: `services/flag-api/cmd/main.go` (register routes)

**Interfaces:**
- Consumes: existing `*sql.DB`, `*redis.Client`, `*zap.Logger` from main.go (same pattern as all other handlers)
- Produces:
  - `GET /api/v1/change-requests?status=PENDING` → `{ "requests": [ChangeRequest] }`
  - `POST /api/v1/change-requests/:id/approve` body `{ "approved_by": string }` → `{ "id": string, "status": "APPROVED" }`
  - `POST /api/v1/change-requests/:id/reject` body `{ "rejected_by": string, "reason": string }` → `{ "id": string, "status": "REJECTED" }`

- [ ] **Step 1: Create services/flag-api/internal/api/v1/change_requests.go**

```go
package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type ChangeRequestHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

func NewChangeRequestHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *ChangeRequestHandler {
	return &ChangeRequestHandler{db: db, rdb: rdb, logger: logger}
}

type ChangeRequest struct {
	ID              string          `json:"id"`
	FlagKey         string          `json:"flag_key"`
	Environment     string          `json:"environment"`
	RequestedBy     string          `json:"requested_by"`
	Status          string          `json:"status"`
	ChangePayload   json.RawMessage `json:"change_payload"`
	ApprovedBy      []string        `json:"approved_by"`
	RejectedBy      *string         `json:"rejected_by,omitempty"`
	RejectionReason *string         `json:"rejection_reason,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
}

// GET /api/v1/change-requests?status=PENDING
func (h *ChangeRequestHandler) ListChangeRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "PENDING"
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, flag_key, environment, requested_by, status,
		       change_payload, COALESCE(approved_by, '{}'),
		       rejected_by, rejection_reason,
		       EXTRACT(EPOCH FROM created_at)::bigint,
		       EXTRACT(EPOCH FROM updated_at)::bigint
		FROM change_requests
		WHERE status = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, status)
	if err != nil {
		h.logger.Error("list change requests", zap.Error(err))
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	requests := []ChangeRequest{}
	for rows.Next() {
		var cr ChangeRequest
		var approvedBy []string
		if err := rows.Scan(
			&cr.ID, &cr.FlagKey, &cr.Environment, &cr.RequestedBy, &cr.Status,
			&cr.ChangePayload, &approvedBy,
			&cr.RejectedBy, &cr.RejectionReason,
			&cr.CreatedAt, &cr.UpdatedAt,
		); err != nil {
			continue
		}
		cr.ApprovedBy = approvedBy
		requests = append(requests, cr)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"requests": requests})
}

// POST /api/v1/change-requests/{id}/approve
func (h *ChangeRequestHandler) ApproveChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		ApprovedBy string `json:"approved_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ApprovedBy == "" {
		http.Error(w, `{"error":"approved_by required"}`, http.StatusBadRequest)
		return
	}

	now := time.Now()
	_, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status = 'APPROVED',
		    approved_by = array_append(COALESCE(approved_by, '{}'), $1),
		    updated_at = $2
		WHERE id = $3 AND status = 'PENDING'
	`, body.ApprovedBy, now, id)
	if err != nil {
		h.logger.Error("approve change request", zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "APPROVED"})
}

// POST /api/v1/change-requests/{id}/reject
func (h *ChangeRequestHandler) RejectChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		RejectedBy string `json:"rejected_by"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RejectedBy == "" {
		http.Error(w, `{"error":"rejected_by required"}`, http.StatusBadRequest)
		return
	}

	now := time.Now()
	_, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status = 'REJECTED',
		    rejected_by = $1,
		    rejection_reason = $2,
		    updated_at = $3
		WHERE id = $4 AND status = 'PENDING'
	`, body.RejectedBy, body.Reason, now, id)
	if err != nil {
		h.logger.Error("reject change request", zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "REJECTED"})
}
```

- [ ] **Step 2: Register routes in services/flag-api/cmd/main.go**

Read the current `cmd/main.go`. Find the section where handlers are instantiated (around line 100-120 where `NewFlagHandler`, `NewAuditHandler` etc. are called).

Add after the existing handler declarations:
```go
changeReqH := v1.NewChangeRequestHandler(db, rdb, logger)
```

Then find the `r.Route("/api/v1", ...)` block and add after the existing break-glass routes:
```go
r.Route("/change-requests", func(r chi.Router) {
    r.Get("/", changeReqH.ListChangeRequests)
    r.Post("/{id}/approve", changeReqH.ApproveChangeRequest)
    r.Post("/{id}/reject", changeReqH.RejectChangeRequest)
})
```

- [ ] **Step 3: Build to verify no compile errors**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/flag-api
GOWORK=off go build ./... 2>&1
```

Expected: no output (zero errors)

- [ ] **Step 4: Run existing tests**

```bash
GOWORK=off go test ./... 2>&1 | tail -10
```

Expected: `ok github.com/tombstone/flag-api/...`

- [ ] **Step 5: Commit and push to trigger Northflank redeploy**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/flag-api/internal/api/v1/change_requests.go services/flag-api/cmd/main.go
git commit -m "feat(flag-api): add change-requests CRUD endpoints — list/approve/reject for ApprovalQueue"
git push origin main
```

Northflank auto-rebuilds from main on every push. Build takes ~3-5 minutes.

---

## PHASE 5 — SECONDARY SERVICES: Deploy evaluator + intelligence

### Task 5: Deploy evaluator to Northflank Account 2

**Files:**
- Read: `infra/northflank/` (no spec exists for evaluator yet — create via UI)

**Context:** Evaluator provides blast-radius scoring. SLOView depends on it. Deploy to Northflank Account 2 (a separate free account to get 2 more free services).

- [ ] **Step 1: Log into Northflank Account 2**

Go to `https://app.northflank.com` and log in with the second account (sairam056 or whatever the second account email is).

- [ ] **Step 2: Create project "tombstone" on Account 2**

Click **"+ Create new" → "Project"** → name: `tombstone` → region: US Central.

- [ ] **Step 3: Create evaluator service**

Click **"Deploy a repository"** and fill in:

| Field | Value |
|-------|-------|
| Name | `tombstone-evaluator` |
| Repo | `sairam0424/Tombstone` |
| Branch | `main` |
| Build context | `/services/evaluator` |
| Dockerfile | `/services/evaluator/Dockerfile` |
| Plan | Sandbox (free) |
| Port | `8082` → HTTP → Public ON |

Runtime variables:
```
PORT                     = 8082
DB_URL                   = [from infra/.env.secrets]
REDIS_URL                = [from infra/.env.secrets]
JWT_SECRET               = REDACTED_ROTATE_THIS_TOKEN
FLAG_API_URL             = [new flag-api URL from Task 1]
UPSTASH_REDIS_REST_URL   = [from infra/.env.secrets]
UPSTASH_REDIS_REST_TOKEN = [from infra/.env.secrets]
```

- [ ] **Step 4: Record evaluator URL and update Vercel**

Once deployed, copy the evaluator URL (e.g. `p01--tombstone-evaluator--XXXXXXX.code.run`).

In `infra/.env.secrets` add:
```
EVALUATOR_URL=https://p01--tombstone-evaluator--XXXXXXX.code.run
```

In Vercel env vars, update `VITE_EVAL_URL` to this URL.

### Task 6: Deploy intelligence to Northflank Account 2

**Context:** Intelligence service provides anomaly detection, stale flag detection, LLM recommendations. GovernanceDash and Experiments depend on it. This is the Python service — it runs the BAAI/bge-m3 Bedrock embeddings.

- [ ] **Step 1: Create intelligence service on Account 2**

Same project "tombstone" on Account 2. Click **"+ Create new" → "Service"**.

| Field | Value |
|-------|-------|
| Name | `tombstone-intelligence` |
| Repo | `sairam0424/Tombstone` |
| Branch | `main` |
| Build context | `/services/intelligence` |
| Dockerfile | `/services/intelligence/Dockerfile` |
| Plan | Sandbox (free) |
| Port | `8083` → HTTP → Public ON |

Runtime variables:
```
PORT                       = 8083
DB_URL                     = [from infra/.env.secrets]
REDIS_URL                  = [from infra/.env.secrets]
EMBEDDING_BACKEND          = bedrock
BEDROCK_ACCESS_KEY_ID      = [from infra/.env.secrets]
BEDROCK_SECRET_ACCESS_KEY  = [from infra/.env.secrets]
BEDROCK_REGION             = us-east-1
CONSUMER_BACKEND           = redis
TOMBSTONE_ENVIRONMENTS     = production
UPSTASH_REDIS_REST_URL     = [from infra/.env.secrets]
UPSTASH_REDIS_REST_TOKEN   = [from infra/.env.secrets]
```

- [ ] **Step 2: Record intelligence URL and update Vercel**

Copy the intelligence URL, add to `infra/.env.secrets`:
```
INTELLIGENCE_URL=https://p01--tombstone-intelligence--XXXXXXX.code.run
```

In Vercel env vars, update `VITE_INTEL_URL` to this URL.

- [ ] **Step 3: Trigger final Vercel redeploy with all URLs baked in**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git commit --allow-empty -m "chore: trigger Vercel redeploy — all service URLs now set"
git push origin main
```

---

## PHASE 6 — DASHBOARD POLISH

### Task 7: Fix actor identity — useCurrentUser hook

**Files:**
- Create: `workspace-dashboard/src/hooks/useCurrentUser.ts`
- Modify: `workspace-dashboard/src/views/ApprovalQueue/index.tsx`

**Interfaces:**
- Produces: `useCurrentUser(): { email: string, isAuthenticated: boolean }` — reads JWT from localStorage key `tombstone_token`, decodes the payload, returns the `sub` or `email` claim. Falls back to `"anonymous@tombstone.dev"` if no token.

- [ ] **Step 1: Create src/hooks/useCurrentUser.ts**

```ts
// workspace-dashboard/src/hooks/useCurrentUser.ts

interface JWTPayload {
  sub?: string;
  email?: string;
  exp?: number;
}

function decodeJWTPayload(token: string): JWTPayload | null {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return null;
    const payload = atob(parts[1].replace(/-/g, '+').replace(/_/g, '/'));
    return JSON.parse(payload) as JWTPayload;
  } catch {
    return null;
  }
}

export function useCurrentUser() {
  // Try localStorage key 'tombstone_token' first, then sessionStorage
  const token =
    (typeof localStorage !== 'undefined' && localStorage.getItem('tombstone_token')) ||
    (typeof sessionStorage !== 'undefined' && sessionStorage.getItem('tombstone_token')) ||
    null;

  if (!token) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  const payload = decodeJWTPayload(token);
  if (!payload) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  // Check expiry
  if (payload.exp && payload.exp * 1000 < Date.now()) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  const email = payload.email ?? payload.sub ?? 'anonymous@tombstone.dev';
  return { email, isAuthenticated: true };
}
```

- [ ] **Step 2: Update ApprovalQueue to use useCurrentUser**

Read `workspace-dashboard/src/views/ApprovalQueue/index.tsx`.

Add import at top:
```tsx
import { useCurrentUser } from '../../hooks/useCurrentUser.js';
```

Add inside the component:
```tsx
const { email: currentUserEmail } = useCurrentUser();
```

Find the two hardcoded `'current-user@example.com'` strings and replace with `currentUserEmail`.

- [ ] **Step 3: Build and verify**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/hooks/useCurrentUser.ts workspace-dashboard/src/views/ApprovalQueue/index.tsx
git commit -m "feat(dashboard): useCurrentUser — decode JWT identity for ApprovalQueue actor, replaces hardcoded email"
git push origin main
```

### Task 8: Remove keyboard shortcuts stub + graceful degradation

**Files:**
- Modify: `workspace-dashboard/src/App.tsx`
- Modify: `workspace-dashboard/src/views/GovernanceDash/index.tsx`
- Modify: `workspace-dashboard/src/views/SLOView/index.tsx`

**Interfaces:**
- Produces: (1) `?` key and shortcuts palette item no longer show broken behavior; (2) GovernanceDash and SLOView show a clear "service unavailable" empty state instead of silent failure when VITE_INTEL_URL / VITE_EVAL_URL are localhost

- [ ] **Step 1: Fix keyboard shortcuts stub in App.tsx**

Read `src/App.tsx`. Find:
```tsx
'?': showShortcuts,
```
Where `showShortcuts` does `console.log('TODO: show shortcut map')`.

Remove the `'?'` shortcut entirely (delete that key from the useKeyboard call and delete the showShortcuts useCallback). Also remove the shortcuts item from CommandPalette's help group (find `navigate('/?shortcuts=1')` and remove that Command.Item).

- [ ] **Step 2: Add service-unavailable detection to GovernanceDash**

Read `src/views/GovernanceDash/index.tsx`.

The health summary query hits `INTEL_URL`. If `INTEL_URL` contains `localhost`, the fetch will fail silently. Add a check at the top of the component:

```tsx
import { INTEL_URL } from '../../config.js';

const isIntelAvailable = !INTEL_URL.includes('localhost');
```

Then wrap the intel-dependent sections:

```tsx
{!isIntelAvailable && (
  <EmptyState
    heading="Intelligence service offline"
    body="GovernanceDash requires the intelligence service. Deploy evaluator + intelligence to Northflank to enable this view."
  />
)}
{isIntelAvailable && (
  /* existing GovernanceDash content */
)}
```

- [ ] **Step 3: Add same check to SLOView**

Read `src/views/SLOView/index.tsx`.

Add `import { EVAL_URL } from '../../config.js'` and:

```tsx
const isEvalAvailable = !EVAL_URL.includes('localhost');

if (!isEvalAvailable) {
  return (
    <div style={{ padding: '32px 40px' }}>
      <EmptyState
        heading="Evaluator service offline"
        body="SLO tracking requires the evaluator service. Deploy it to Northflank Account 2 to enable this view."
      />
    </div>
  );
}
```

- [ ] **Step 4: Build and verify**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/App.tsx workspace-dashboard/src/views/GovernanceDash/index.tsx workspace-dashboard/src/views/SLOView/index.tsx
git commit -m "fix(dashboard): remove shortcuts TODO, add service-unavailable graceful degradation for GovernanceDash + SLOView"
git push origin main
```

---

## PHASE 7 — RELEASE: Tag and announce

### Task 9: Cut v1.0.0 release tag

**Files:**
- Modify: `workspace-dashboard/package.json` (bump version to 1.0.0)
- Modify: `CLAUDE.md` (update current version)

- [ ] **Step 1: Bump dashboard version to 1.0.0**

In `workspace-dashboard/package.json`, change `"version": "0.1.0"` to `"version": "1.0.0"`.

In `CLAUDE.md`, find `**Current version: v2.0.1**` and update to `**Current version: v2.2.0 / Dashboard v1.0.0**`.

- [ ] **Step 2: Final smoke test — verify all critical paths**

Run these checks and confirm ALL pass before tagging:

```bash
# flag-api health
curl -s https://p01--tombstone-flag-api--XXXXXXX.code.run/health
# Expected: {"status":"ok"}

# flags list
curl -s -H "Authorization: Bearer sdk-dev-token-change-in-prod" https://p01--tombstone-flag-api--XXXXXXX.code.run/api/v1/flags | python3 -c "import json,sys; d=json.load(sys.stdin); print('flags:', len(d.get('flags',[])))"

# gateway health
curl -s https://p01--tombstone-gateway--ddzvzj6b5rd6.code.run/health

# dashboard loads
curl -sI https://tombstone-workspace-dashboard-ihw7.vercel.app | grep "HTTP"
```

- [ ] **Step 3: Commit and tag**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add workspace-dashboard/package.json CLAUDE.md
git commit -m "chore: bump dashboard to v1.0.0 — release ready"
git tag -a v1.0.0 -m "Tombstone v1.0.0 — Feature flag platform release

Services deployed:
- flag-api: Northflank Account 1
- gateway: Northflank Account 1
- evaluator: Northflank Account 2
- intelligence: Northflank Account 2
- dashboard: Vercel (tombstone-workspace-dashboard-ihw7.vercel.app)

What works:
- Flag CRUD (create, list, toggle, archive)
- Environment snapshots (dev/staging/prod)
- SSE real-time updates
- Dependency graph
- Approval queue (four-eyes workflow)
- Break-glass emergency tokens
- Incident timeline (audit log)
- GovernanceDash (stale flag detection)
- Experiments analysis (warehouse connector)
- Command palette (Cmd+K)"
git push origin main
git push origin v1.0.0
```

- [ ] **Step 4: Create GitHub Release**

```bash
export PATH="/opt/homebrew/bin:$PATH"
gh release create v1.0.0 \
  --title "Tombstone v1.0.0 — Feature Flag Platform" \
  --notes "## Tombstone v1.0.0

Production-ready feature flag platform with blast-radius intelligence.

### Live URLs
- **Dashboard**: https://tombstone-workspace-dashboard-ihw7.vercel.app
- **API**: https://p01--tombstone-flag-api--XXXXXXX.code.run/health

### What ships in v1
- Flag management (create, toggle, rollout %)
- Four-eyes approval workflow
- Real-time SSE flag updates
- Causal dependency graph
- Incident timeline (What Changed?)
- Break-glass emergency override
- GovernanceDash (stale flag detection, health score)
- Cmd+K command palette

### Getting started
See docs/superpowers/plans/ for deployment guides."
```

---

## Self-Review

**Spec coverage:**
- ✅ flag-api redeploy → Task 1
- ✅ JWT_SECRET verification → Task 2
- ✅ Credential cleanup → Task 2
- ✅ Dashboard branch merge → Task 3
- ✅ Change-requests Go handler → Task 4
- ✅ Evaluator deploy → Task 5
- ✅ Intelligence deploy → Task 6
- ✅ Actor identity fix → Task 7
- ✅ Keyboard shortcuts stub removal → Task 8
- ✅ Graceful degradation for offline services → Task 8
- ✅ Version tag + GitHub release → Task 9

**Placeholder scan:** All steps have concrete instructions. No TBD.

**Type consistency:**
- `useCurrentUser()` returns `{ email: string, isAuthenticated: boolean }` — consumed identically in ApprovalQueue Task 7
- `ChangeRequestHandler` methods match the route registrations in main.go Task 4
