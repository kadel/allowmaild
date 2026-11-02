// Package store persists request/idempotency rows in SQLite.
//
// One table holds the idempotency state machine, the audit log, and the
// source of truth for rate limiting (limits are computed by counting
// reservation rows in the window). Only requests that passed validation and
// rate checks are ever inserted; the insert is the atomic commit point.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StateSending   = "sending"
	StateSent      = "sent"
	StateFailed    = "failed"
	StateAmbiguous = "ambiguous"
)

const schema = `
CREATE TABLE IF NOT EXISTS requests (
	key          TEXT PRIMARY KEY,
	request_id   TEXT NOT NULL,
	alias        TEXT NOT NULL,
	subject_hash TEXT NOT NULL,
	body_hash    TEXT NOT NULL,
	state        TEXT NOT NULL CHECK (state IN ('sending','sent','failed','ambiguous')),
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	result_code  TEXT NOT NULL DEFAULT '',
	message_id   TEXT NOT NULL DEFAULT '',
	approved     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_requests_created_at ON requests (created_at);
CREATE INDEX IF NOT EXISTS idx_requests_alias_created_at ON requests (alias, created_at);
`

type Store struct {
	db *sql.DB
}

// Row is a stored request record.
type Row struct {
	Key         string
	RequestID   string
	Alias       string
	SubjectHash string
	BodyHash    string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ResultCode  string
	MessageID   string
	// Approved records whether the request asserted human approval.
	Approved bool
}

// Open opens (creating if needed) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes writers, which makes the
	// rate-check + reserve transaction race-free.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate brings databases created by older schema versions up to date.
// Pre-approval databases lack the approved column; the default marks their
// rows as unasserted.
func migrate(db *sql.DB) error {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name = 'approved'`).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(
			`ALTER TABLE requests ADD COLUMN approved INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// RateLimits are the effective limits for one reservation attempt.
// Recipient limits of zero mean "not configured" and are not checked.
type RateLimits struct {
	GlobalPerHour    int
	GlobalPerDay     int
	RecipientPerHour int
	RecipientPerDay  int
}

// ReserveParams describes one reservation attempt.
type ReserveParams struct {
	Key         string
	RequestID   string
	Alias       string
	SubjectHash string
	BodyHash    string
	Now         time.Time
	Limits      RateLimits
	// Approved is the request's approval assertion, persisted for audit.
	// It does not participate in duplicate/content matching.
	Approved bool
}

// ReserveKind classifies the outcome of Reserve.
type ReserveKind int

const (
	// Reserved means a new row was inserted in state "sending".
	Reserved ReserveKind = iota
	// Replay means the key exists in a terminal state with matching hashes.
	Replay
	// KeyReuse means the key exists but the content hashes differ.
	KeyReuse
	// InFlight means the key exists and is still in state "sending".
	InFlight
	// RateLimited means a configured limit is exhausted; Limit names it.
	RateLimited
)

type ReserveResult struct {
	Kind     ReserveKind
	Existing *Row   // set for Replay, KeyReuse, InFlight
	Limit    string // set for RateLimited, e.g. "per_hour"
}

// Reserve atomically checks for duplicates and rate limits and, when clear,
// inserts the request row in state "sending". Everything runs in one write
// transaction so concurrent requests cannot double-spend the key or budget.
func (s *Store) Reserve(ctx context.Context, p ReserveParams) (ReserveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReserveResult{}, err
	}
	defer tx.Rollback()

	existing, err := getRow(ctx, tx, p.Key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ReserveResult{}, err
	}
	if existing != nil {
		switch {
		case existing.SubjectHash != p.SubjectHash || existing.BodyHash != p.BodyHash:
			return ReserveResult{Kind: KeyReuse, Existing: existing}, nil
		case existing.State == StateSending:
			return ReserveResult{Kind: InFlight, Existing: existing}, nil
		default:
			return ReserveResult{Kind: Replay, Existing: existing}, nil
		}
	}

	if limit, err := checkLimits(ctx, tx, p); err != nil {
		return ReserveResult{}, err
	} else if limit != "" {
		return ReserveResult{Kind: RateLimited, Limit: limit}, nil
	}

	now := p.Now.Unix()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO requests (key, request_id, alias, subject_hash, body_hash, state, created_at, updated_at, approved)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Key, p.RequestID, p.Alias, p.SubjectHash, p.BodyHash, StateSending, now, now, p.Approved)
	if err != nil {
		return ReserveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReserveResult{}, err
	}
	return ReserveResult{Kind: Reserved}, nil
}

func checkLimits(ctx context.Context, tx *sql.Tx, p ReserveParams) (string, error) {
	hourAgo := p.Now.Add(-time.Hour).Unix()
	dayAgo := p.Now.Add(-24 * time.Hour).Unix()

	type check struct {
		name  string
		limit int
		query string
		args  []any
	}
	countGlobal := `SELECT COUNT(*) FROM requests WHERE created_at > ?`
	countAlias := `SELECT COUNT(*) FROM requests WHERE alias = ? AND created_at > ?`
	checks := []check{
		{"per_hour", p.Limits.GlobalPerHour, countGlobal, []any{hourAgo}},
		{"per_day", p.Limits.GlobalPerDay, countGlobal, []any{dayAgo}},
		{"recipient per_hour", p.Limits.RecipientPerHour, countAlias, []any{p.Alias, hourAgo}},
		{"recipient per_day", p.Limits.RecipientPerDay, countAlias, []any{p.Alias, dayAgo}},
	}
	for _, c := range checks {
		if c.limit <= 0 {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, c.query, c.args...).Scan(&n); err != nil {
			return "", err
		}
		if n >= c.limit {
			return c.name, nil
		}
	}
	return "", nil
}

// Complete moves a reserved row to its terminal state.
func (s *Store) Complete(ctx context.Context, key, state, resultCode, messageID string, now time.Time) error {
	if state != StateSent && state != StateFailed && state != StateAmbiguous {
		return fmt.Errorf("not a terminal state: %q", state)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET state = ?, result_code = ?, message_id = ?, updated_at = ?
		 WHERE key = ? AND state = ?`,
		state, resultCode, messageID, now.Unix(), key, StateSending)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("request row was not in state sending")
	}
	return nil
}

// SweepSending transitions rows stuck in "sending" to "ambiguous". It runs at
// startup: a crash mid-attempt leaves delivery unknowable.
func (s *Store) SweepSending(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET state = ?, result_code = 'swept', updated_at = ? WHERE state = ?`,
		StateAmbiguous, now.Unix(), StateSending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Purge deletes rows created before cutoff.
func (s *Store) Purge(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE created_at < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Get returns the row for key, or sql.ErrNoRows.
func (s *Store) Get(ctx context.Context, key string) (*Row, error) {
	return getRow(ctx, s.db, key)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getRow(ctx context.Context, q querier, key string) (*Row, error) {
	var r Row
	var created, updated int64
	err := q.QueryRowContext(ctx,
		`SELECT key, request_id, alias, subject_hash, body_hash, state, created_at, updated_at, result_code, message_id, approved
		 FROM requests WHERE key = ?`, key).
		Scan(&r.Key, &r.RequestID, &r.Alias, &r.SubjectHash, &r.BodyHash, &r.State,
			&created, &updated, &r.ResultCode, &r.MessageID, &r.Approved)
	if err != nil {
		return nil, err
	}
	r.CreatedAt = time.Unix(created, 0)
	r.UpdatedAt = time.Unix(updated, 0)
	return &r, nil
}
