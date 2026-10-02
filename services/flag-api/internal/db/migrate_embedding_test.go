package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Migration 028 retypes flags.embedding from vector(768) to vector(1024) and
// swaps the baseline ivfflat index for HNSW. A fresh install already starts in
// that end state (schema.sql), where 028 must change nothing, so each upgrade
// branch -- the ones existing deployments run -- is reached only by rewinding the
// column and index to a pre-028 state first. These helpers back subtests of
// TestMigrationRunner (which owns the pristine database). Each rewind registers
// a cleanup that puts the end state back even when the subtest fails midway, so
// one failure does not cascade into unrelated errors in the next subtest.

const (
	embeddingTypeWant    = "vector(1024)"
	embeddingIndex       = "idx_flags_embedding"
	embeddingIndexMethod = "hnsw"

	// SQLSTATE lock_not_available: what Postgres raises when lock_timeout expires.
	lockNotAvailableCode = "55P03"

	ivfflatIndexDDL = `CREATE INDEX idx_flags_embedding ON flags USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100)`
)

// embeddingState is a pre-028 state of flags.embedding and idx_flags_embedding.
// An empty column or method means the column or index is absent.
type embeddingState struct {
	rewind string
	column string
	method string
}

var (
	// What every database built from the old schema.sql holds.
	state768Ivfflat = embeddingState{
		rewind: `DROP INDEX IF EXISTS idx_flags_embedding;
			ALTER TABLE flags ALTER COLUMN embedding TYPE vector(768);
			` + ivfflatIndexDDL,
		column: "vector(768)",
		method: "ivfflat",
	}
	// A column fixed by hand that still carries the old index.
	state1024Ivfflat = embeddingState{
		rewind: `DROP INDEX IF EXISTS idx_flags_embedding; ` + ivfflatIndexDDL,
		column: embeddingTypeWant,
		method: "ivfflat",
	}
	state1024NoIndex = embeddingState{
		rewind: `DROP INDEX IF EXISTS idx_flags_embedding`,
		column: embeddingTypeWant,
	}
	// A database built before migration 008 and adopted with -baseline.
	stateNoColumn = embeddingState{
		rewind: `ALTER TABLE flags DROP COLUMN embedding`,
	}
)

func migrationSQL(t *testing.T, version int64) string {
	t.Helper()
	migs, err := allMigrations()
	if err != nil {
		t.Fatalf("allMigrations: %v", err)
	}
	for _, m := range migs {
		if m.version == version {
			return m.sql
		}
	}
	t.Fatalf("migration %d not found", version)
	return ""
}

// readEmbeddingState returns flags.embedding's type and idx_flags_embedding's
// access method, "" for an absent column or index. It reports an error instead
// of failing the test so a cleanup can inspect a half-migrated database.
func readEmbeddingState(ctx context.Context, database *sql.DB) (column, method string, err error) {
	err = database.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		                  WHERE attrelid = 'flags'::regclass AND attname = 'embedding' AND NOT attisdropped), ''),
		       COALESCE((SELECT am.amname FROM pg_class c JOIN pg_am am ON am.oid = c.relam
		                  WHERE c.oid = to_regclass('idx_flags_embedding')), '')`).Scan(&column, &method)
	return column, method, err
}

func requireEmbeddingState(ctx context.Context, t *testing.T, database *sql.DB, wantColumn, wantMethod string) {
	t.Helper()
	column, method, err := readEmbeddingState(ctx, database)
	if err != nil {
		t.Fatalf("read flags.embedding state: %v", err)
	}
	if column != wantColumn || method != wantMethod {
		t.Fatalf("flags.embedding is %q with index method %q, want %q with %q", column, method, wantColumn, wantMethod)
	}
}

// restoreEmbeddingState puts flags.embedding back to vector(1024) with the HNSW
// index. It is written independently of migration 028 on purpose: it has to work
// when 028 is the thing that is broken.
func restoreEmbeddingState(ctx context.Context, t *testing.T, database *sql.DB) {
	column, method, err := readEmbeddingState(ctx, database)
	if err == nil && column == embeddingTypeWant && method == embeddingIndexMethod {
		return
	}
	const restore = `
		DROP INDEX IF EXISTS idx_flags_embedding;
		ALTER TABLE flags ADD COLUMN IF NOT EXISTS embedding vector(1024);
		UPDATE flags SET embedding = NULL WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 1024;
		ALTER TABLE flags ALTER COLUMN embedding TYPE vector(1024);
		CREATE INDEX idx_flags_embedding ON flags USING hnsw (embedding vector_cosine_ops)`
	if _, err := database.ExecContext(ctx, restore); err != nil {
		t.Errorf("restore flags.embedding to vector(1024) with an HNSW index: %v", err)
	}
}

// rewindEmbedding puts flags.embedding into a pre-028 state. Cleanups run last
// in, first out, so a flag inserted afterwards is deleted before the restore.
// The column must hold no values when the rewind narrows it.
func rewindEmbedding(ctx context.Context, t *testing.T, database *sql.DB, state embeddingState) {
	t.Helper()
	t.Cleanup(func() { restoreEmbeddingState(ctx, t, database) })
	if _, err := database.ExecContext(ctx, state.rewind); err != nil {
		t.Fatalf("rewind flags.embedding to %q with index method %q: %v", state.column, state.method, err)
	}
	requireEmbeddingState(ctx, t, database, state.column, state.method)
}

// insertFlagWithEmbedding inserts a throwaway project and flag whose embedding
// has the given number of dimensions, removes both when the subtest ends, and
// returns the flag's key.
func insertFlagWithEmbedding(ctx context.Context, t *testing.T, database *sql.DB, dimensions int) string {
	t.Helper()
	const key = "migration-028-flag"
	var projectID string
	err := database.QueryRowContext(ctx,
		`INSERT INTO projects (name, slug) VALUES ('migration-028', 'migration-028') RETURNING id`).Scan(&projectID)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := database.ExecContext(ctx, `DELETE FROM flags WHERE project_id = $1`, projectID); err != nil {
			t.Errorf("delete flag: %v", err)
		}
		if _, err := database.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, projectID); err != nil {
			t.Errorf("delete project: %v", err)
		}
	})
	_, err = database.ExecContext(ctx,
		`INSERT INTO flags (key, project_id, name, flag_type, owner_id, embedding)
		 VALUES ($1, $2, 'migration 028', 'BOOLEAN', 'migration-test',
		         array_fill(0.1::real, ARRAY[$3::int])::vector)`,
		key, projectID, dimensions)
	if err != nil {
		t.Fatalf("insert flag with a %d-dimensional embedding: %v", dimensions, err)
	}
	return key
}

func embeddingDimensions(ctx context.Context, t *testing.T, database *sql.DB, key string) sql.NullInt64 {
	t.Helper()
	var dims sql.NullInt64
	err := database.QueryRowContext(ctx, `SELECT vector_dims(embedding) FROM flags WHERE key = $1`, key).Scan(&dims)
	if err != nil {
		t.Fatalf("read embedding dimensions of %q: %v", key, err)
	}
	return dims
}

func indexDefinition(ctx context.Context, t *testing.T, database *sql.DB, name string) string {
	t.Helper()
	var def string
	if err := database.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name).Scan(&def); err != nil {
		t.Fatalf("read definition of index %q: %v", name, err)
	}
	return def
}

func indexOID(ctx context.Context, t *testing.T, database *sql.DB, name string) int64 {
	t.Helper()
	var oid int64
	if err := database.QueryRowContext(ctx, `SELECT $1::regclass::oid::bigint`, name).Scan(&oid); err != nil {
		t.Fatalf("read oid of index %q: %v", name, err)
	}
	return oid
}

func applyMigration028(ctx context.Context, t *testing.T, database *sql.DB) {
	t.Helper()
	// No args -> lib/pq uses the simple query protocol, as applyOne does.
	if _, err := database.ExecContext(ctx, migrationSQL(t, 28)); err != nil {
		t.Fatalf("apply migration 028: %v", err)
	}
}

// holdFlagsLock opens a transaction that locks flags in the given mode until the
// returned release func runs (or the test ends).
func holdFlagsLock(ctx context.Context, t *testing.T, database *sql.DB, mode string) (release func()) {
	t.Helper()
	holder, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback() }) // no-op once released
	if _, err := holder.ExecContext(ctx, `LOCK TABLE flags IN `+mode+` MODE`); err != nil {
		t.Fatalf("hold flags in %s mode: %v", mode, err)
	}
	return func() {
		if err := holder.Rollback(); err != nil {
			t.Fatalf("release flags: %v", err)
		}
	}
}

// testEmbedding028Upgrade: on a vector(768) column with the ivfflat index 028
// retypes the column, sets a stray non-1024-dimensional value to NULL and
// replaces the index with the HNSW definition schema.sql gives a fresh install.
func testEmbedding028Upgrade(ctx context.Context, t *testing.T, database *sql.DB) {
	freshIndexDef := indexDefinition(ctx, t, database, embeddingIndex)
	rewindEmbedding(ctx, t, database, state768Ivfflat)
	key := insertFlagWithEmbedding(ctx, t, database, 768)

	applyMigration028(ctx, t, database)

	requireEmbeddingState(ctx, t, database, embeddingTypeWant, embeddingIndexMethod)
	if dims := embeddingDimensions(ctx, t, database, key); dims.Valid {
		t.Fatalf("stray 768-dimensional embedding survived 028 as %d dimensions, want NULL", dims.Int64)
	}
	if got := indexDefinition(ctx, t, database, embeddingIndex); got != freshIndexDef {
		t.Fatalf("index after 028 = %q, want the fresh-install definition %q", got, freshIndexDef)
	}
}

// testEmbedding028ReplacesIvfflatOn1024: a column already fixed by hand keeps its
// data, but its ivfflat index (random lists, poor recall) is still replaced.
func testEmbedding028ReplacesIvfflatOn1024(ctx context.Context, t *testing.T, database *sql.DB) {
	rewindEmbedding(ctx, t, database, state1024Ivfflat)
	key := insertFlagWithEmbedding(ctx, t, database, 1024)

	applyMigration028(ctx, t, database)

	requireEmbeddingState(ctx, t, database, embeddingTypeWant, embeddingIndexMethod)
	if dims := embeddingDimensions(ctx, t, database, key); !dims.Valid || dims.Int64 != 1024 {
		t.Fatalf("1024-dimensional embedding after 028 = %+v, want 1024 dimensions", dims)
	}
}

// testEmbedding028AddsAMissingColumn: a database adopted with -baseline from
// before migration 008 has no flags.embedding; 028 adds it instead of failing on
// "column embedding does not exist", which would block every later migration.
func testEmbedding028AddsAMissingColumn(ctx context.Context, t *testing.T, database *sql.DB) {
	rewindEmbedding(ctx, t, database, stateNoColumn)

	applyMigration028(ctx, t, database)

	requireEmbeddingState(ctx, t, database, embeddingTypeWant, embeddingIndexMethod)
}

// testEmbedding028Rerun: on a column that is already vector(1024) with the HNSW
// index -- `make migrate` re-applies every file -- 028 keeps valid embeddings and
// leaves the index alone rather than dropping and rebuilding it.
func testEmbedding028Rerun(ctx context.Context, t *testing.T, database *sql.DB) {
	key := insertFlagWithEmbedding(ctx, t, database, 1024)
	indexBefore := indexOID(ctx, t, database, embeddingIndex)

	applyMigration028(ctx, t, database)

	if dims := embeddingDimensions(ctx, t, database, key); !dims.Valid || dims.Int64 != 1024 {
		t.Fatalf("1024-dimensional embedding after a 028 re-run = %+v, want 1024 dimensions", dims)
	}
	if indexAfter := indexOID(ctx, t, database, embeddingIndex); indexAfter != indexBefore {
		t.Fatalf("028 re-run rebuilt %s (oid %d -> %d), want it left alone", embeddingIndex, indexBefore, indexAfter)
	}
}

// testEmbedding028RerunTakesNoTableLock: on a database already in the target
// state 028 only reads catalogs, so it must finish while another transaction
// holds the strongest lock on flags. (An unconditional CREATE INDEX IF NOT
// EXISTS would wait for a SHARE lock and queue behind the holder.)
func testEmbedding028RerunTakesNoTableLock(ctx context.Context, t *testing.T, database *sql.DB) {
	release := holdFlagsLock(ctx, t, database, "ACCESS EXCLUSIVE")
	defer release()

	// Shorter than the migration's own 5s lock_timeout, so a wait fails here.
	rerunCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := database.ExecContext(rerunCtx, migrationSQL(t, 28)); err != nil {
		t.Fatalf("028 re-run while another transaction holds flags: %v", err)
	}
}

// testEmbedding028GivesUpOnAHeldLock: with another transaction holding a lock
// that conflicts with 028's first change, 028 must fail fast on its lock_timeout,
// change nothing, and succeed once the holder is gone -- not queue behind it and
// stall every later reader of flags for longer than that timeout. The second case
// reaches the final CREATE INDEX, which must be bounded by the same timeout.
func testEmbedding028GivesUpOnAHeldLock(ctx context.Context, t *testing.T, database *sql.DB) {
	cases := []struct {
		name  string
		state embeddingState
		mode  string
	}{
		{"drop of the old index", state768Ivfflat, "ACCESS SHARE"},
		{"creation of the missing index", state1024NoIndex, "ROW EXCLUSIVE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewindEmbedding(ctx, t, database, tc.state)
			release := holdFlagsLock(ctx, t, database, tc.mode)

			// Bounded so a missing lock_timeout fails this test instead of hanging it.
			blockedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := database.ExecContext(blockedCtx, migrationSQL(t, 28))
			var pqErr *pq.Error
			if !errors.As(err, &pqErr) || pqErr.Code != lockNotAvailableCode {
				t.Fatalf("028 against a held table: err = %v, want SQLSTATE %s (lock_timeout)", err, lockNotAvailableCode)
			}

			release()
			requireEmbeddingState(ctx, t, database, tc.state.column, tc.state.method)

			applyMigration028(ctx, t, database)
			requireEmbeddingState(ctx, t, database, embeddingTypeWant, embeddingIndexMethod)
		})
	}
}
