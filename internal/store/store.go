// Package store persists the job record and audit trail in Postgres. The
// AcceleratorJob CRD in Kubernetes remains the source of truth for execution
// state; Postgres exists for queryable history and idempotency-key dedup.
package store

import (
	"context"
	"errors"
	"fmt"
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

func (s *Store) ListJobs(ctx context.Context, limit int) ([]JobRecord, error) {
	const q = `SELECT id, idempotency_key, team, image, accelerator_type, accelerator_count, priority, status, created_at, updated_at FROM jobs ORDER BY created_at DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
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
