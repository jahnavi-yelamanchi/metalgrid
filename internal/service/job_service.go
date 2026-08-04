// Package service holds the platform's transport-agnostic business logic —
// the REST handlers (internal/api) and the gRPC server (internal/grpcapi)
// are both thin wrappers around JobService so job semantics live in one
// place instead of being duplicated per transport.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/controller"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/store"
)

var ErrNotFound = store.ErrNotFound
var ErrValidation = errors.New("validation failed")

const DefaultPageLimit = 50
const MaxPageLimit = 100

type JobService struct {
	Store     *store.Store
	Queue     *queue.Queue
	K8s       client.Client
	Namespace string
	Logger    *slog.Logger
}

type CreateJobInput struct {
	Team             string
	Image            string
	Command          []string
	Args             []string
	AcceleratorType  string
	AcceleratorCount int32
	Priority         int32
	IdempotencyKey   string
}

func (in CreateJobInput) Validate() error {
	if in.Team == "" || in.Image == "" || in.AcceleratorType == "" || in.AcceleratorCount < 1 {
		return fmt.Errorf("%w: team, image, acceleratorType and acceleratorCount are required", ErrValidation)
	}
	return nil
}

type Job struct {
	ID               string
	Team             string
	Image            string
	AcceleratorType  string
	AcceleratorCount int32
	Priority         int32
	Status           string
	Message          string
	CreatedAt        time.Time
}

func jobFromRecord(rec store.JobRecord) Job {
	return Job{
		ID:               rec.ID,
		Team:             rec.Team,
		Image:            rec.Image,
		AcceleratorType:  rec.AcceleratorType,
		AcceleratorCount: rec.AcceleratorCount,
		Priority:         rec.Priority,
		Status:           rec.Status,
		CreatedAt:        rec.CreatedAt,
	}
}

// CreateJob records the job, then enqueues it for the operator to pick up.
// A repeated IdempotencyKey returns the original job without re-enqueuing.
func (s *JobService) CreateJob(ctx context.Context, in CreateJobInput) (Job, error) {
	if err := in.Validate(); err != nil {
		return Job{}, err
	}

	idempotencyKey := in.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}
	id := "job-" + uuid.NewString()

	rec, err := s.Store.CreateJob(ctx, store.JobRecord{
		ID:               id,
		IdempotencyKey:   idempotencyKey,
		Team:             in.Team,
		Image:            in.Image,
		AcceleratorType:  in.AcceleratorType,
		AcceleratorCount: in.AcceleratorCount,
		Priority:         in.Priority,
	})
	if err != nil {
		return Job{}, fmt.Errorf("recording job: %w", err)
	}

	// Only publish (and audit) on first insert; a replayed idempotency key
	// returns the existing record without re-enqueuing or double-auditing.
	if rec.ID == id {
		payload, err := json.Marshal(queue.JobSubmission{
			ID:               rec.ID,
			Team:             rec.Team,
			Image:            rec.Image,
			Command:          in.Command,
			Args:             in.Args,
			AcceleratorType:  rec.AcceleratorType,
			AcceleratorCount: rec.AcceleratorCount,
			Priority:         rec.Priority,
		})
		if err != nil {
			return Job{}, fmt.Errorf("encoding job submission: %w", err)
		}
		if err := s.Queue.Publish(ctx, idempotencyKey, payload); err != nil {
			return Job{}, fmt.Errorf("enqueuing job: %w", err)
		}
		if err := s.Store.RecordAudit(ctx, rec.ID, rec.Team, "create"); err != nil {
			s.Logger.Error("recording audit entry", "error", err, "job", rec.ID)
		}
	}

	return jobFromRecord(rec), nil
}

// GetJob loads the stored record and overlays the AcceleratorJob CRD's live
// status, if it's reconciled far enough to have one.
func (s *JobService) GetJob(ctx context.Context, id string) (Job, error) {
	rec, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	job := jobFromRecord(rec)

	var cr metalgridv1alpha1.AcceleratorJob
	if err := s.K8s.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: id}, &cr); err == nil {
		if cr.Status.Phase != "" {
			job.Status = string(cr.Status.Phase)
		}
		job.Message = cr.Status.Message
	}
	return job, nil
}

// ListJobs returns a page of jobs (Postgres-only; no live k8s status merge,
// so listing many jobs doesn't cost a k8s Get per row) plus a cursor for the
// next page, empty once there isn't one. team scopes results to that
// tenant; callers driven by an authenticated request should always pass
// the caller's team so tenants can't list each other's jobs.
func (s *JobService) ListJobs(ctx context.Context, team, cursor string, limit int) ([]Job, string, error) {
	if limit <= 0 || limit > MaxPageLimit {
		limit = DefaultPageLimit
	}

	var after *store.Cursor
	if cursor != "" {
		c, err := store.DecodeCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor: %v", ErrValidation, err)
		}
		after = &c
	}

	recs, err := s.Store.ListJobsPage(ctx, team, after, limit)
	if err != nil {
		return nil, "", err
	}

	hasMore := len(recs) > limit
	if hasMore {
		recs = recs[:limit]
	}

	jobs := make([]Job, len(recs))
	for i, rec := range recs {
		jobs[i] = jobFromRecord(rec)
	}

	nextCursor := ""
	if hasMore {
		last := recs[len(recs)-1]
		nextCursor = store.EncodeCursor(store.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return jobs, nextCursor, nil
}

// DeleteJob cancels a job: deleting the CRD lets the reconciler's finalizer
// tear it down cleanly, same path as a normal completion.
func (s *JobService) DeleteJob(ctx context.Context, id string) error {
	job := &metalgridv1alpha1.AcceleratorJob{}
	err := s.K8s.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: id}, job)
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("loading job: %w", err)
	}

	if err := s.K8s.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting job: %w", err)
	}
	_ = s.Store.UpdateStatus(ctx, id, "Cancelled")
	if err := s.Store.RecordAudit(ctx, id, job.Spec.Team, "cancel"); err != nil {
		s.Logger.Error("recording audit entry", "error", err, "job", id)
	}
	return nil
}

type Capacity struct {
	Total     int64
	Available int64
}

func (s *JobService) Capacity(ctx context.Context) (Capacity, error) {
	var nodes corev1.NodeList
	if err := s.K8s.List(ctx, &nodes); err != nil {
		return Capacity{}, fmt.Errorf("listing nodes: %w", err)
	}

	var total, available resource.Quantity
	for _, n := range nodes.Items {
		if cap, ok := n.Status.Capacity[controller.AcceleratorResourceName]; ok {
			total.Add(cap)
		}
		if alloc, ok := n.Status.Allocatable[controller.AcceleratorResourceName]; ok {
			available.Add(alloc)
		}
	}
	return Capacity{Total: total.Value(), Available: available.Value()}, nil
}

type DLQEntry struct {
	Submission *queue.JobSubmission
	Raw        string
}

// ListDLQ returns dead-lettered payloads. Entries that aren't well-formed
// JobSubmission JSON come back as Raw instead of being dropped — those are
// often exactly the ones that dead-lettered *because* they failed to parse,
// so hiding them would erase the one thing an operator needs to see.
func (s *JobService) ListDLQ(ctx context.Context, limit int) ([]DLQEntry, error) {
	payloads, err := s.Queue.PeekDLQ(ctx, limit)
	if err != nil {
		return nil, err
	}
	return parseDLQEntries(payloads), nil
}

func parseDLQEntries(payloads [][]byte) []DLQEntry {
	entries := make([]DLQEntry, 0, len(payloads))
	for _, p := range payloads {
		var sub queue.JobSubmission
		if err := json.Unmarshal(p, &sub); err != nil {
			entries = append(entries, DLQEntry{Raw: string(p)})
			continue
		}
		entries = append(entries, DLQEntry{Submission: &sub})
	}
	return entries
}

type AuditEntry struct {
	JobID     string
	Team      string
	Action    string
	CreatedAt time.Time
}

func (s *JobService) ListAudit(ctx context.Context, cursor string, limit int) ([]AuditEntry, string, error) {
	if limit <= 0 || limit > MaxPageLimit {
		limit = DefaultPageLimit
	}

	var after *store.Cursor
	if cursor != "" {
		c, err := store.DecodeCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor: %v", ErrValidation, err)
		}
		after = &c
	}

	recs, err := s.Store.ListAuditPage(ctx, after, limit)
	if err != nil {
		return nil, "", err
	}

	hasMore := len(recs) > limit
	if hasMore {
		recs = recs[:limit]
	}

	entries := make([]AuditEntry, len(recs))
	for i, rec := range recs {
		entries[i] = AuditEntry{JobID: rec.JobID, Team: rec.Team, Action: rec.Action, CreatedAt: rec.CreatedAt}
	}

	nextCursor := ""
	if hasMore {
		last := recs[len(recs)-1]
		nextCursor = store.EncodeCursor(store.Cursor{CreatedAt: last.CreatedAt, ID: fmt.Sprint(last.ID)})
	}
	return entries, nextCursor, nil
}
