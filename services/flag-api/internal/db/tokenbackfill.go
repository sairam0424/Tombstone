package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/tombstone/flag-api/internal/secrets"
)

// TokenBackfillResult reports how many rows were converted per table.
type TokenBackfillResult struct {
	ServiceTokens    int
	BreakGlassTokens int
}

// hashableTables are the credential tables migration 014 converts from plaintext
// to keyed hashes. Both are keyed by a bearer token with no other identifier.
var hashableTables = []string{"service_tokens", "break_glass_tokens"}

// HashExistingTokens derives token_hash from the surviving plaintext token for
// every row that still has one, then NULLs the plaintext (SEC-4, step 2 of
// migration 014).
//
// It is idempotent: rows whose plaintext is already erased are skipped, so a
// re-run converts nothing and reports zero. Each table is converted in a single
// transaction, so a failure cannot leave a row with neither a hash nor its
// plaintext — which would silently destroy a live credential.
func HashExistingTokens(ctx context.Context, database *sql.DB, hasher *secrets.TokenHasher) (TokenBackfillResult, error) {
	var result TokenBackfillResult
	counts := make(map[string]int, len(hashableTables))

	for _, table := range hashableTables {
		n, err := hashTable(ctx, database, hasher, table)
		if err != nil {
			return result, fmt.Errorf("hash %s: %w", table, err)
		}
		counts[table] = n
	}

	result.ServiceTokens = counts["service_tokens"]
	result.BreakGlassTokens = counts["break_glass_tokens"]
	return result, nil
}

func hashTable(ctx context.Context, database *sql.DB, hasher *secrets.TokenHasher, table string) (int, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// table is from the package-level hashableTables allowlist, never user input.
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, token FROM %s WHERE token IS NOT NULL`, table)) //nolint:gosec // fixed allowlist
	if err != nil {
		return 0, err
	}

	type pending struct {
		id   string
		hash string
	}
	var todo []pending
	for rows.Next() {
		var id, token string
		if err := rows.Scan(&id, &token); err != nil {
			_ = rows.Close()
			return 0, err
		}
		todo = append(todo, pending{id: id, hash: hasher.Hash(token)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	for _, p := range todo {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET token_hash = $1, token = NULL WHERE id = $2`, table), //nolint:gosec // fixed allowlist
			p.hash, p.id); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(todo), nil
}

// CountPlaintextTokens reports how many credential rows still hold a plaintext
// token. It is the assertion used to prove SEC-4 actually took effect: after the
// backfill this must be zero.
func CountPlaintextTokens(ctx context.Context, database *sql.DB) (int, error) {
	total := 0
	for _, table := range hashableTables {
		var n int
		if err := database.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE token IS NOT NULL`, table), //nolint:gosec // fixed allowlist
		).Scan(&n); err != nil {
			return 0, fmt.Errorf("count plaintext in %s: %w", table, err)
		}
		total += n
	}
	return total, nil
}
