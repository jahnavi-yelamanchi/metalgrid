// Package store persists the job record and audit trail in Postgres. The
// AcceleratorJob CRD in Kubernetes remains the source of truth for execution
// state; Postgres exists for queryable history and idempotency-key dedup.
package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("job not found")

type JobRecord struct {
	ID               string
	IdempotencyKey   string
	Team             string
	Image            string
	AcceleratorType  string
	AcceleratorCount int32
	Priority         int32
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Ping checks Postgres reachability, for readiness probes.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Close() {
	s.pool.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id                text PRIMARY KEY,
	idempotency_key   text UNIQUE NOT NULL,
	team              text NOT NULL,
	image             text NOT NULL,
	accelerator_type  text NOT NULL,
	accelerator_count integer NOT NULL,
	priority          integer NOT NULL DEFAULT 0,
	status            text NOT NULL DEFAULT 'Pending',
	created_at        timestamptz NOT NULL DEFAULT now(),
	updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS jobs_created_at_idx ON jobs (created_at DESC);

CREATE TABLE IF NOT EXISTS audit_log (
	id         bigserial PRIMARY KEY,
	job_id     text NOT NULL,
	team       text NOT NULL,
	action     text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_log_created_at_idx ON audit_log (created_at DESC);
`

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schema)
	if err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	return nil
}

// CreateJob inserts a job row. If idempotencyKey was already used, it returns
// the existing row instead of erroring, so retried submissions are safe.
func (s *Store) CreateJob(ctx context.Context, rec JobRecord) (JobRecord, error) {
	const insert = `
INSERT INTO jobs (id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'Pending')
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status, created_at, updated_at`

	row := s.pool.QueryRow(ctx, insert, rec.ID, rec.IdempotencyKey, rec.Team, rec.Image, rec.AcceleratorType, rec.AcceleratorCount, rec.Priority)
	out, err := scanJob(row)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return JobRecord{}, fmt.Errorf("inserting job: %w", err)
	}

	// Conflict: another request already used this idempotency key.
	return s.GetJobByIdempotencyKey(ctx, rec.IdempotencyKey)
}

func (s *Store) GetJob(ctx context.Context, id string) (JobRecord, error) {
	const q = `SELECT id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status, created_at, updated_at FROM jobs WHERE id = $1`
	return scanJob(s.pool.QueryRow(ctx, q, id))
}

func (s *Store) GetJobByIdempotencyKey(ctx context.Context, key string) (JobRecord, error) {
	const q = `SELECT id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status, created_at, updated_at FROM jobs WHERE idempotency_key = $1`
	return scanJob(s.pool.QueryRow(ctx, q, key))
}

// Cursor marks a position in the (created_at, id) ordering used for
// pagination — the pair is unique and monotonic even when many rows share a
// created_at timestamp.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodeCursor produces an opaque, URL-safe pagination token.
func EncodeCursor(c Cursor) string {
	raw := fmt.Sprintf("%s|%s", c.CreatedAt.Format(time.RFC3339Nano), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by EncodeCursor.
func DecodeCursor(token string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("decoding cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return Cursor{}, fmt.Errorf("malformed cursor")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, fmt.Errorf("malformed cursor timestamp: %w", err)
	}
	return Cursor{CreatedAt: ts, ID: parts[1]}, nil
}

// ListJobsPage returns up to limit+1 jobs older than after (newest first,
// tie-broken by id), so the caller can detect a next page by checking for
// the extra row rather than issuing a separate count query. team, if
// non-empty, scopes results to that tenant.
func (s *Store) ListJobsPage(ctx context.Context, team string, after *Cursor, limit int) ([]JobRecord, error) {
	const cols = `id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status, created_at, updated_at`
	var rows pgx.Rows
	var err error
	switch {
	case team == "" && after == nil:
		rows, err = s.pool.Query(ctx, `SELECT `+cols+` FROM jobs ORDER BY created_at DESC, id DESC LIMIT $1`, limit+1)
	case team == "":
		rows, err = s.pool.Query(ctx,
			`SELECT `+cols+` FROM jobs WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT $3`,
			after.CreatedAt, after.ID, limit+1)
	case after == nil:
		rows, err = s.pool.Query(ctx, `SELECT `+cols+` FROM jobs WHERE team = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, team, limit+1)
	default:
		rows, err = s.pool.Query(ctx,
			`SELECT `+cols+` FROM jobs WHERE team = $1 AND (created_at, id) < ($2, $3) ORDER BY created_at DESC, id DESC LIMIT $4`,
			team, after.CreatedAt, after.ID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close()

	var out []JobRecord
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

type AuditRecord struct {
	ID        int64
	JobID     string
	Team      string
	Action    string
	CreatedAt time.Time
}

// RecordAudit appends an audit trail entry for a job mutation.
func (s *Store) RecordAudit(ctx context.Context, jobID, team, action string) error {
	const q = `INSERT INTO audit_log (job_id, team, action) VALUES ($1, $2, $3)`
	_, err := s.pool.Exec(ctx, q, jobID, team, action)
	if err != nil {
		return fmt.Errorf("recording audit entry: %w", err)
	}
	return nil
}

// ListAuditPage mirrors ListJobsPage's cursor pagination over audit_log.
func (s *Store) ListAuditPage(ctx context.Context, after *Cursor, limit int) ([]AuditRecord, error) {
	const cols = `id, job_id, team, action, created_at`
	var rows pgx.Rows
	var err error
	if after == nil {
		rows, err = s.pool.Query(ctx, `SELECT `+cols+` FROM audit_log ORDER BY created_at DESC, id DESC LIMIT $1`, limit+1)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+cols+` FROM audit_log WHERE (created_at, id::text) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT $3`,
			after.CreatedAt, after.ID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("listing audit log: %w", err)
	}
	defer rows.Close()

	var out []AuditRecord
	for rows.Next() {
		var rec AuditRecord
		if err := rows.Scan(&rec.ID, &rec.JobID, &rec.Team, &rec.Action, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning audit row: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) UpdateStatus(ctx context.Context, id, status string) error {
	const q = `UPDATE jobs SET status = $2, updated_at = now() WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, status)
	if err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (JobRecord, error) {
	var rec JobRecord
	err := row.Scan(&rec.ID, &rec.IdempotencyKey, &rec.Team, &rec.Image, &rec.AcceleratorType, &rec.AcceleratorCount, &rec.Priority, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return JobRecord{}, ErrNotFound
	}
	if err != nil {
		return JobRecord{}, fmt.Errorf("scanning job row: %w", err)
	}
	return rec, nil
}
