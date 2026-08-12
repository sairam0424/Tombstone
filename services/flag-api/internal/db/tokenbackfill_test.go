package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/tombstone/flag-api/internal/secrets"
)

// TestHashExistingTokens proves SEC-4 end-to-end against a REAL Postgres: a
// plaintext credential present before the backfill is unreadable afterwards, and
// the stored hash is exactly what the auth path will compute from the presented
// token. It runs in the flag-api-migrations CI job (TEST_DATABASE_URL) and skips
// locally otherwise.
func TestHashExistingTokens(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed token backfill test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The schema (including migration 014's token_hash columns) must exist.
	// Migrate is idempotent, so this is safe regardless of test ordering.
	if _, err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	hasher, err := secrets.NewTokenHasher("backfill-test-pepper-" + strings.Repeat("0", 16))
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	// Seed a project (service_tokens.project_id is a FK) plus one plaintext
	// credential in each hashable table, mimicking a pre-SEC-4 database.
	var projectID string
	if err := database.QueryRowContext(ctx, `
		INSERT INTO projects (name, slug) VALUES ('sec4-test', 'sec4-test-'||gen_random_uuid())
		RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	svcPlain := "svc-plain-" + projectID
	bgPlain := "bgt_plain-" + projectID

	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_, _ = database.ExecContext(cleanupCtx, `DELETE FROM service_tokens WHERE project_id = $1`, projectID)
		_, _ = database.ExecContext(cleanupCtx, `DELETE FROM break_glass_tokens WHERE created_by = $1`, svcPlain)
		_, _ = database.ExecContext(cleanupCtx, `DELETE FROM projects WHERE id = $1`, projectID)
	})

	if _, err := database.ExecContext(ctx, `
		INSERT INTO service_tokens (project_id, environment, token, name, role)
		VALUES ($1, 'development', $2, 'sec4-svc', 'OPERATOR')`, projectID, svcPlain); err != nil {
		t.Fatalf("seed service token: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO break_glass_tokens (token, scope, created_by, expires_at)
		VALUES ($1, 'all-flags', $2, now() + interval '4 hours')`, bgPlain, svcPlain); err != nil {
		t.Fatalf("seed break-glass token: %v", err)
	}

	// Sanity: the plaintext really is readable before the backfill, which is the
	// vulnerability being closed.
	if n, err := CountPlaintextTokens(ctx, database); err != nil || n < 2 {
		t.Fatalf("expected >=2 plaintext tokens before backfill (got %d, err %v)", n, err)
	}

	res, err := HashExistingTokens(ctx, database, hasher)
	if err != nil {
		t.Fatalf("HashExistingTokens: %v", err)
	}
	if res.ServiceTokens < 1 || res.BreakGlassTokens < 1 {
		t.Fatalf("converted service=%d break-glass=%d, want >=1 each", res.ServiceTokens, res.BreakGlassTokens)
	}

	t.Run("no plaintext token survives", func(t *testing.T) {
		n, err := CountPlaintextTokens(ctx, database)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Fatalf("%d row(s) still hold a plaintext token — a DB dump would still yield credentials", n)
		}
	})

	t.Run("stored hash matches what the auth path computes", func(t *testing.T) {
		var gotHash string
		var gotPlain sql.NullString
		if err := database.QueryRowContext(ctx,
			`SELECT token_hash, token FROM service_tokens WHERE project_id = $1`, projectID,
		).Scan(&gotHash, &gotPlain); err != nil {
			t.Fatalf("read back service token: %v", err)
		}
		if gotPlain.Valid {
			t.Error("plaintext token column must be NULL after backfill")
		}
		if gotHash != hasher.Hash(svcPlain) {
			t.Error("stored hash must equal HMAC(pepper, original token) — otherwise the token stops authenticating")
		}
	})

	t.Run("break-glass token is hashed too", func(t *testing.T) {
		var gotHash string
		if err := database.QueryRowContext(ctx,
			`SELECT token_hash FROM break_glass_tokens WHERE created_by = $1`, svcPlain,
		).Scan(&gotHash); err != nil {
			t.Fatalf("read back break-glass token: %v", err)
		}
		if gotHash != hasher.Hash(bgPlain) {
			t.Error("break-glass hash mismatch")
		}
	})

	t.Run("re-running converts nothing (idempotent)", func(t *testing.T) {
		again, err := HashExistingTokens(ctx, database, hasher)
		if err != nil {
			t.Fatalf("second HashExistingTokens: %v", err)
		}
		if again.ServiceTokens != 0 || again.BreakGlassTokens != 0 {
			t.Fatalf("second run converted service=%d break-glass=%d, want 0 — must be safe to re-run",
				again.ServiceTokens, again.BreakGlassTokens)
		}
	})
}
