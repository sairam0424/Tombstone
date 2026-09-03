package audit

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tombstone/flag-api/internal/secrets"
)

// Retention archives whole months of audit_log out of the live table once
// they age past a configured window (DATA-2).
//
// "Archive" means DETACH PARTITION + rename, not DROP: no_audit_update/
// no_audit_delete (schema.sql) make it impossible to remove rows any other
// way — that IS what makes the log append-only — but detaching keeps the
// data itself intact and queryable under its new name rather than
// destroying it. This is a deliberately minimal cold tier: it needs no new
// infrastructure (object storage, a lifecycle policy, a restore path) to
// build. Exporting an archived table to object storage and dropping the
// local copy after a much longer legal-hold window is a natural next step —
// left for when a real compliance requirement drives it; there is no live
// production deployment yet to justify building it speculatively.
type Retention struct {
	db  *sql.DB
	key *secrets.AuditKey
}

// NewRetention builds the retention archiver. It shares audit_log's signing
// key rather than a dedicated one — whoever can forge a retention checkpoint
// could already forge chain entries with the same key, so a second key would
// add rotation cost without closing any additional attack surface (unlike
// COMPLIANCE_SIGNING_KEY, which is deliberately separate from JWT_SECRET
// because an export verifier and a token minter must never share a key —
// there is no analogous new party here).
func NewRetention(db *sql.DB, key *secrets.AuditKey) *Retention {
	return &Retention{db: db, key: key}
}

var partitionNamePattern = regexp.MustCompile(`^audit_log_(\d{4})_(\d{2})$`)

func partitionName(monthStart time.Time) string {
	return fmt.Sprintf("audit_log_%04d_%02d", monthStart.Year(), monthStart.Month())
}

// pgIdent safely quotes a Postgres identifier for interpolation into DDL.
// Every caller in this file builds the identifier itself from a regex-
// validated partition name or an internally computed month — never from
// request input — but DDL cannot be parameterized the way DML can (Postgres'
// partition-bound and identifier grammar requires literal tokens, not bind
// parameters), so this quoting is what stands in for parameterization here.
func pgIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// EnsurePartitions creates the monthly partition for `from`'s month and each
// of the next monthsAhead months, if they don't already exist. Postgres does
// not auto-create partitions — a row whose created_at falls in a month with
// no partition yet would otherwise silently land in audit_log_default (the
// catch-all required so INSERT never fails outright), where it is never a
// candidate for archiving by month. Calling this ahead of time keeps that
// default partition empty in steady-state operation.
func (r *Retention) EnsurePartitions(ctx context.Context, from time.Time, monthsAhead int) error {
	start := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= monthsAhead; i++ {
		lower := start.AddDate(0, i, 0)
		upper := lower.AddDate(0, 1, 0)
		name := partitionName(lower)
		// FOR VALUES FROM/TO requires a literal, not a bind parameter — see
		// pgIdent's comment. lower/upper are computed above from `from` and
		// `i`, never from request input.
		ddl := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF audit_log FOR VALUES FROM ('%s') TO ('%s')`,
			pgIdent(name), lower.Format(time.RFC3339), upper.Format(time.RFC3339),
		)
		if _, err := r.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("ensure partition %s: %w", name, err)
		}
	}
	return nil
}

// partitionInfo is one monthly partition eligible for archiving.
type partitionInfo struct {
	name  string
	lower time.Time
	upper time.Time
}

// discoverArchivablePartitions finds every audit_log_YYYY_MM partition whose
// entire range is older than olderThan, oldest first. audit_log_default (and
// anything else not matching the naming EnsurePartitions uses) is skipped —
// it is the catch-all partition and must never be archived, since rows
// outside any explicit monthly range fall into it by construction.
func (r *Retention) discoverArchivablePartitions(ctx context.Context, olderThan time.Time) ([]partitionInfo, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT child.relname
		FROM pg_inherits
		JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
		JOIN pg_class child ON pg_inherits.inhrelid = child.oid
		WHERE parent.relname = 'audit_log'
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []partitionInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		m := partitionNamePattern.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		lower := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		upper := lower.AddDate(0, 1, 0)
		if !upper.After(olderThan) {
			out = append(out, partitionInfo{name: name, lower: lower, upper: upper})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].lower.Before(out[j].lower) })
	return out, nil
}

// ArchiveReport summarizes one Archive run.
type ArchiveReport struct {
	PartitionsArchived []string `json:"partitions_archived"`
	CheckpointsWritten int      `json:"checkpoints_written"`

	// StrandedInDefaultPartition surfaces DefaultPartitionRowCount so a
	// caller (RunRetention's HTTP response, surfaced by the loop script)
	// sees this otherwise-permanently-silent condition — rows in
	// audit_log_default because EnsurePartitions fell behind can never be
	// archived by month once there — rather than it only living in a
	// comment. Zero in steady-state operation.
	StrandedInDefaultPartition int        `json:"stranded_in_default_partition"`
	StrandedSince              *time.Time `json:"stranded_since,omitempty"`
}

// Archive detaches every fully-eligible monthly partition (oldest first) and
// renames it to "<name>_archived", sealing a signed checkpoint first for any
// chain that has a row surviving past the partition being archived — see
// checkpointCanonical and Verify's use of audit_retention_checkpoints for why.
func (r *Retention) Archive(ctx context.Context, olderThan time.Time) (ArchiveReport, error) {
	var report ArchiveReport
	if r.key == nil {
		return report, fmt.Errorf("audit retention has no signing key — refusing to archive without a way to checkpoint chain gaps")
	}

	partitions, err := r.discoverArchivablePartitions(ctx, olderThan)
	if err != nil {
		return report, fmt.Errorf("discover archivable partitions: %w", err)
	}

	for _, p := range partitions {
		n, err := r.archiveOne(ctx, p)
		if err != nil {
			return report, fmt.Errorf("archive partition %s: %w", p.name, err)
		}
		report.PartitionsArchived = append(report.PartitionsArchived, p.name)
		report.CheckpointsWritten += n
	}

	strandedCount, oldest, err := r.DefaultPartitionRowCount(ctx)
	if err != nil {
		return report, fmt.Errorf("check default partition: %w", err)
	}
	report.StrandedInDefaultPartition = strandedCount
	if strandedCount > 0 {
		report.StrandedSince = &oldest
	}

	return report, nil
}

// DefaultPartitionRowCount reports how many rows currently sit in
// audit_log_default and, if any, the oldest one's created_at. Rows land
// there when EnsurePartitions falls behind — e.g. the retention loop paused
// (AUDIT_RETENTION_ADMIN_TOKEN unset, per scripts/loop-audit-retention.sh's
// own skip-not-fail behavior) for longer than its lookahead window. Postgres
// never retroactively re-routes already-committed DEFAULT-partition rows
// into a monthly partition created later, so once stranded there, this
// package can never archive them by month.
func (r *Retention) DefaultPartitionRowCount(ctx context.Context) (int, time.Time, error) {
	var count int
	var oldest sql.NullTime
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(created_at) FROM audit_log_default`,
	).Scan(&count, &oldest); err != nil {
		return 0, time.Time{}, err
	}
	return count, oldest.Time, nil
}

// archiveOne archives a single partition: lock every candidate chain against
// concurrent Append (see the advisory-lock loop below), seal checkpoints for
// chains that continue past it, then detach and rename — all in one
// transaction, so a partition can never end up detached with its checkpoints
// missing (which would make Verify falsely report tampering) or vice versa,
// AND never with a checkpoint decision made from a survivor snapshot a
// concurrent write has since invalidated.
func (r *Retention) archiveOne(ctx context.Context, p partitionInfo) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// The newest hashed row per chain within this partition — the exact tip
	// a checkpoint must vouch for, since it is what the chain's next live row
	// (if any) already has recorded as ITS prev_hash. id DESC breaks a
	// created_at tie the SAME way Writer.Append's own chain-tip lookup does
	// (audit.go) — without it, Postgres's DISTINCT ON gives no guarantee
	// which of two same-instant rows it returns, and picking the wrong one
	// would checkpoint a hash that never actually matches the surviving
	// row's real prev_hash, causing a false tampering report on legitimately
	// archived data.
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT ON (COALESCE(project_id::text,''), COALESCE(flag_key,''))
		    COALESCE(project_id::text,''), COALESCE(flag_key,''), entry_hash, created_at
		FROM audit_log
		WHERE created_at >= $1 AND created_at < $2 AND entry_hash IS NOT NULL
		ORDER BY COALESCE(project_id::text,''), COALESCE(flag_key,''), created_at DESC, id DESC
	`, p.lower, p.upper)
	if err != nil {
		return 0, fmt.Errorf("find chain tips in partition: %w", err)
	}

	type tip struct {
		projectID, flagKey, entryHash string
		createdAt                     time.Time
	}
	var tips []tip
	flagKeySet := map[string]bool{}
	for rows.Next() {
		var t tip
		if err := rows.Scan(&t.projectID, &t.flagKey, &t.entryHash, &t.createdAt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		tips = append(tips, t)
		flagKeySet[t.flagKey] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	_ = rows.Close()

	// Serialize this partition's checkpoint-vs-DETACH decision against any
	// concurrent Writer.Append to the same flag_key(s), using the SAME
	// advisory lock Append itself takes (audit.go) before its chain-tip
	// read+insert. Without this, survivingChains below takes its snapshot
	// under plain READ COMMITTED — a fresh per-statement snapshot, not a
	// whole-transaction one — so a real Append that lands between that
	// snapshot and this transaction's later DETACH could commit a new row
	// for a chain survivingChains had already (correctly, as of its own
	// snapshot) marked as having no survivor, causing this code to skip a
	// checkpoint that row now needs, and Verify to report a false tampering
	// failure against data that was legitimately archived. Locking every
	// candidate flag_key here, in sorted order (to avoid a lock-ordering
	// deadlock against another archiveOne call touching an overlapping
	// set), blocks until any in-flight Append to it has committed or been
	// blocked out, so the survivor check that follows is guaranteed stable
	// through to this transaction's own commit.
	sortedFlagKeys := make([]string, 0, len(flagKeySet))
	for fk := range flagKeySet {
		sortedFlagKeys = append(sortedFlagKeys, fk)
	}
	sort.Strings(sortedFlagKeys)
	for _, fk := range sortedFlagKeys {
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock($1, hashtext($2))`, advisoryNamespace, fk); err != nil {
			return 0, fmt.Errorf("lock chain %q for archive: %w", fk, err)
		}
	}

	// Chains that have at least one row still live past this partition's
	// upper bound — only these need a checkpoint. A chain fully contained in
	// archived partitions has nothing left to explain a gap to: if it is
	// ever appended to again, Append's chain-tip lookup finds no live row and
	// the new entry starts a fresh, empty-prev_hash genesis — indistinguishable
	// from (and semantically equivalent to) a brand-new flag, which needs no
	// checkpoint either.
	survivors, err := r.survivingChains(ctx, tx, p.upper)
	if err != nil {
		return 0, fmt.Errorf("find surviving chains: %w", err)
	}

	written := 0
	for _, t := range tips {
		if !survivors[chainKey(t.projectID, t.flagKey)] {
			continue
		}
		signature := r.key.Sum(checkpointCanonical(t.projectID, t.flagKey, t.entryHash, t.createdAt))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_retention_checkpoints
			    (project_id, flag_key, pruned_through_hash, pruned_through_created_at, signature)
			VALUES ($1, $2, $3, $4, $5)
		`, nullIfEmpty(t.projectID), t.flagKey, t.entryHash, t.createdAt, signature); err != nil {
			return 0, fmt.Errorf("write checkpoint for chain %s/%s: %w", t.projectID, t.flagKey, err)
		}
		written++
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE audit_log DETACH PARTITION %s`, pgIdent(p.name),
	)); err != nil {
		return 0, fmt.Errorf("detach partition: %w", err)
	}

	archivedName := p.name + "_archived"
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s RENAME TO %s`, pgIdent(p.name), pgIdent(archivedName),
	)); err != nil {
		return 0, fmt.Errorf("rename archived partition: %w", err)
	}

	// A detached partition does not inherit the parent's RULEs going
	// forward — it is a standalone table now. An archived table must stay
	// exactly as immutable as the live one it came from, so the same
	// append-only guard is reapplied under its own names (a table can't
	// share a rule name across the whole database on the same rule OID
	// unless explicitly namespaced per-table, which CREATE RULE already
	// does: rule names are scoped to the table they're defined on).
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`CREATE OR REPLACE RULE no_archive_update AS ON UPDATE TO %s DO INSTEAD NOTHING`, pgIdent(archivedName),
	)); err != nil {
		return 0, fmt.Errorf("reapply update guard: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`CREATE OR REPLACE RULE no_archive_delete AS ON DELETE TO %s DO INSTEAD NOTHING`, pgIdent(archivedName),
	)); err != nil {
		return 0, fmt.Errorf("reapply delete guard: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return written, nil
}

// survivingChains returns the set of chains (as chainKey strings) with at
// least one row at or after cutoff, queried within the same transaction that
// is about to detach the partition below cutoff — so the check and the
// detach see a consistent snapshot.
func (r *Retention) survivingChains(ctx context.Context, tx *sql.Tx, cutoff time.Time) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT COALESCE(project_id::text,''), COALESCE(flag_key,'')
		FROM audit_log
		WHERE created_at >= $1
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var projectID, flagKey string
		if err := rows.Scan(&projectID, &flagKey); err != nil {
			return nil, err
		}
		out[chainKey(projectID, flagKey)] = true
	}
	return out, rows.Err()
}

// chainKey identifies a chain the same way Verify groups rows: by
// (project_id, flag_key). "\x00" cannot appear in a UUID string or a flag
// key, so this concatenation cannot collide two distinct pairs.
func chainKey(projectID, flagKey string) string {
	return projectID + "\x00" + flagKey
}

// checkpointCanonical builds the injective, length-prefixed encoding signed
// into a retention checkpoint — the same style as canonical() for audit
// entries, and for the same reason: distinct checkpoints must never be able
// to serialize identically.
func checkpointCanonical(projectID, flagKey, prunedThroughHash string, prunedThroughCreatedAt time.Time) []byte {
	var b strings.Builder
	for _, f := range []string{
		projectID,
		flagKey,
		prunedThroughHash,
		strconv.FormatInt(prunedThroughCreatedAt.UTC().UnixMicro(), 10),
	} {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return []byte(b.String())
}
