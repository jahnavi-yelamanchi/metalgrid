package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://metalgrid:metalgrid@localhost:5432/metalgrid"
	}
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres not reachable, skipping: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// TestCreateJobIdempotent guards against a regression where a duplicate
// Idempotency-Key returned an error instead of the original job (the
// ON CONFLICT DO NOTHING RETURNING path scans zero rows, which store.CreateJob
// must treat as "fetch the existing row", not a hard failure).
func TestCreateJobIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

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

func TestCursorRoundTrip(t *testing.T) {
	want := Cursor{CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ID: "job-abc"}
	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	if _, err := DecodeCursor("not-a-valid-cursor!!!"); err == nil {
		t.Error("expected an error decoding a garbage cursor")
	}
}

// TestListJobsPagePaginatesInOrder runs against the shared dev Postgres,
// which already has rows from other tests/manual runs — so it can't assume
// an empty table. Instead it pages through everything with a small limit
// and checks that its own 3 seeded jobs come out newest-first with no gaps
// or duplicates, wherever they land relative to pre-existing rows.
func TestListJobsPagePaginatesInOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team := "page-test-" + uuid.NewString()

	var seeded []string
	for i := 0; i < 3; i++ {
		rec, err := s.CreateJob(ctx, JobRecord{
			ID: "job-" + uuid.NewString(), IdempotencyKey: uuid.NewString(),
			Team: team, Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1,
		})
		if err != nil {
			t.Fatalf("CreateJob %d: %v", i, err)
		}
		seeded = append(seeded, rec.ID) // oldest -> newest
	}

	var seenForTeam []string
	seenIDs := map[string]bool{}
	var cursor *Cursor
	for page := 0; page < 50; page++ { // hard cap so a pagination bug can't hang the test
		rows, err := s.ListJobsPage(ctx, "", cursor, 2)
		if err != nil {
			t.Fatalf("ListJobsPage: %v", err)
		}
		hasMore := len(rows) > 2
		if hasMore {
			rows = rows[:2]
		}
		for _, rec := range rows {
			if seenIDs[rec.ID] {
				t.Fatalf("job %s returned twice across pages", rec.ID)
			}
			seenIDs[rec.ID] = true
			if rec.Team == team {
				seenForTeam = append(seenForTeam, rec.ID)
			}
		}
		if !hasMore {
			break
		}
		last := rows[len(rows)-1]
		cursor = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(seenForTeam) != 3 {
		t.Fatalf("expected all 3 seeded jobs across pages, got %v", seenForTeam)
	}
	// Global order is newest-first, so our team's jobs come out newest-to-oldest too.
	for i, id := range seenForTeam {
		if id != seeded[len(seeded)-1-i] {
			t.Errorf("wrong order at position %d: got %s, want %s", i, id, seeded[len(seeded)-1-i])
		}
	}
}

func TestListJobsPageScopesByTeam(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	teamA := "scope-test-a-" + uuid.NewString()
	teamB := "scope-test-b-" + uuid.NewString()

	for _, team := range []string{teamA, teamB} {
		if _, err := s.CreateJob(ctx, JobRecord{
			ID: "job-" + uuid.NewString(), IdempotencyKey: uuid.NewString(),
			Team: team, Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1,
		}); err != nil {
			t.Fatalf("CreateJob for %s: %v", team, err)
		}
	}

	rows, err := s.ListJobsPage(ctx, teamA, nil, 10)
	if err != nil {
		t.Fatalf("ListJobsPage: %v", err)
	}
	for _, r := range rows {
		if r.Team != teamA {
			t.Errorf("expected only %s jobs, got job from team %s", teamA, r.Team)
		}
	}
	found := false
	for _, r := range rows {
		found = found || r.Team == teamA
	}
	if !found {
		t.Error("expected to find the seeded teamA job")
	}
}

func TestAuditLogRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	jobID := "job-" + uuid.NewString()

	if err := s.RecordAudit(ctx, jobID, "platform", "create"); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}

	recs, err := s.ListAuditPage(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListAuditPage: %v", err)
	}

	found := false
	for _, r := range recs {
		if r.JobID == jobID {
			found = true
			if r.Action != "create" || r.Team != "platform" {
				t.Errorf("unexpected audit record %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("expected to find recorded audit entry for %s", jobID)
	}
}
