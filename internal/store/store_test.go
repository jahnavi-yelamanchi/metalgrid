package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestCreateJobIdempotent guards against a regression where a duplicate
// Idempotency-Key returned an error instead of the original job (the
// ON CONFLICT DO NOTHING RETURNING path scans zero rows, which store.CreateJob
// must treat as "fetch the existing row", not a hard failure).
func TestCreateJobIdempotent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://metalgrid:metalgrid@localhost:5432/metalgrid"
	}
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres not reachable, skipping: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	key := uuid.NewString()
	rec := JobRecord{
		ID:               "job-" + uuid.NewString(),
		IdempotencyKey:   key,
		Team:             "test-team",
		Image:            "busybox:1.36",
		AcceleratorType:  "mock-gpu",
		AcceleratorCount: 1,
	}

	first, err := s.CreateJob(ctx, rec)
	if err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}

	retry := rec
	retry.ID = "job-" + uuid.NewString() // simulate a client retry that generated a new job ID
	second, err := s.CreateJob(ctx, retry)
	if err != nil {
		t.Fatalf("second CreateJob (duplicate key) returned error instead of existing row: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("expected duplicate idempotency key to return original job %q, got %q", first.ID, second.ID)
	}
}
