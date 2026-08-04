// Package api implements the MetalGrid platform REST API.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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

type Server struct {
	Store     *store.Store
	Queue     *queue.Queue
	K8s       client.Client
	Namespace string
	Logger    *slog.Logger
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /v1/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("GET /v1/capacity", s.handleCapacity)
	return mux
}

type createJobRequest struct {
	Team             string   `json:"team"`
	Image            string   `json:"image"`
	Command          []string `json:"command,omitempty"`
	Args             []string `json:"args,omitempty"`
	AcceleratorType  string   `json:"acceleratorType"`
	AcceleratorCount int32    `json:"acceleratorCount"`
	Priority         int32    `json:"priority,omitempty"`
}

type jobResponse struct {
	ID               string `json:"id"`
	Team             string `json:"team"`
	Image            string `json:"image"`
	AcceleratorType  string `json:"acceleratorType"`
	AcceleratorCount int32  `json:"acceleratorCount"`
	Priority         int32  `json:"priority"`
	Status           string `json:"status"`
	Message          string `json:"message,omitempty"`
	CreatedAt        string `json:"createdAt"`
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Team == "" || req.Image == "" || req.AcceleratorType == "" || req.AcceleratorCount < 1 {
		writeError(w, http.StatusBadRequest, "team, image, acceleratorType and acceleratorCount are required")
		return
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}

	id := "job-" + uuid.NewString()
	ctx := r.Context()

	rec, err := s.Store.CreateJob(ctx, store.JobRecord{
		ID:               id,
		IdempotencyKey:   idempotencyKey,
		Team:             req.Team,
		Image:            req.Image,
		AcceleratorType:  req.AcceleratorType,
		AcceleratorCount: req.AcceleratorCount,
		Priority:         req.Priority,
	})
	if err != nil {
		s.Logger.Error("creating job record", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record job")
		return
	}

	// Only publish on first insert; a replayed idempotency key returns the
	// existing record without re-enqueuing.
	if rec.ID == id {
		payload, err := json.Marshal(queue.JobSubmission{
			ID:               rec.ID,
			Team:             rec.Team,
			Image:            rec.Image,
			Command:          req.Command,
			Args:             req.Args,
			AcceleratorType:  rec.AcceleratorType,
			AcceleratorCount: rec.AcceleratorCount,
			Priority:         rec.Priority,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode job submission")
			return
		}
		if err := s.Queue.Publish(ctx, idempotencyKey, payload); err != nil {
			s.Logger.Error("publishing job submission", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to enqueue job")
			return
		}
	}

	writeJSON(w, http.StatusCreated, toJobResponse(rec))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.Store.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load job")
		return
	}

	resp := toJobResponse(rec)

	var job metalgridv1alpha1.AcceleratorJob
	if err := s.K8s.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: id}, &job); err == nil {
		if job.Status.Phase != "" {
			resp.Status = string(job.Status.Phase)
		}
		resp.Message = job.Status.Message
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job := &metalgridv1alpha1.AcceleratorJob{}
	err := s.K8s.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: id}, job)
	if apierrors.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load job")
		return
	}

	if err := s.K8s.Delete(r.Context(), job); err != nil && !apierrors.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "failed to delete job")
		return
	}
	_ = s.Store.UpdateStatus(r.Context(), id, "Cancelled")

	w.WriteHeader(http.StatusNoContent)
}

type capacityResponse struct {
	Total     int64 `json:"total"`
	Available int64 `json:"available"`
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	var nodes corev1.NodeList
	if err := s.K8s.List(r.Context(), &nodes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list nodes")
		return
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

	writeJSON(w, http.StatusOK, capacityResponse{
		Total:     total.Value(),
		Available: available.Value(),
	})
}

func toJobResponse(rec store.JobRecord) jobResponse {
	return jobResponse{
		ID:               rec.ID,
		Team:             rec.Team,
		Image:            rec.Image,
		AcceleratorType:  rec.AcceleratorType,
		AcceleratorCount: rec.AcceleratorCount,
		Priority:         rec.Priority,
		Status:           rec.Status,
		CreatedAt:        rec.CreatedAt.Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
