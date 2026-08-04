// Package api implements the MetalGrid platform REST API. Handlers are thin:
// request/response translation only, business logic lives in internal/service
// so the gRPC server (internal/grpcapi) can share it.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jahnavi-yelamanchi/metalgrid/internal/service"
)

type Server struct {
	Jobs      *service.JobService
	Logger    *slog.Logger
	JWTSecret []byte
	// RateLimit and RateBurst configure the per-team token bucket. Zero
	// values disable rate limiting (useful for local dev without a token).
	RateLimit float64
	RateBurst int
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /v1/jobs", s.handleListJobs)
	mux.HandleFunc("GET /v1/jobs/dlq", s.handleListDLQ)
	mux.HandleFunc("GET /v1/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("GET /v1/audit", s.handleListAudit)

	// Capacity is cluster-wide, non-tenant-scoped info; no auth needed.
	public := http.NewServeMux()
	public.HandleFunc("GET /v1/capacity", s.handleCapacity)

	var protected http.Handler = mux
	if len(s.JWTSecret) > 0 {
		if s.RateLimit > 0 {
			protected = newPerTeamLimiter(s.RateLimit, s.RateBurst).Middleware(protected)
		}
		protected = AuthMiddleware(s.JWTSecret, protected)
	}

	root := http.NewServeMux()
	root.Handle("/v1/capacity", public)
	root.Handle("/", protected)
	return root
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

func toJobResponse(j service.Job) jobResponse {
	return jobResponse{
		ID:               j.ID,
		Team:             j.Team,
		Image:            j.Image,
		AcceleratorType:  j.AcceleratorType,
		AcceleratorCount: j.AcceleratorCount,
		Priority:         j.Priority,
		Status:           j.Status,
		Message:          j.Message,
		CreatedAt:        j.CreatedAt.Format(time.RFC3339),
	}
}

// checkTeamAccess enforces that the authenticated token's team claim matches
// the team a request targets. With no JWTSecret configured, auth is off
// (local dev) and every request is allowed through.
func (s *Server) checkTeamAccess(r *http.Request, team string) bool {
	if len(s.JWTSecret) == 0 {
		return true
	}
	authTeam, ok := authenticatedTeam(r.Context())
	return ok && authTeam == team
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !s.checkTeamAccess(r, req.Team) {
		writeError(w, http.StatusForbidden, "token team does not match request team")
		return
	}

	job, err := s.Jobs.CreateJob(r.Context(), service.CreateJobInput{
		Team:             req.Team,
		Image:            req.Image,
		Command:          req.Command,
		Args:             req.Args,
		AcceleratorType:  req.AcceleratorType,
		AcceleratorCount: req.AcceleratorCount,
		Priority:         req.Priority,
		IdempotencyKey:   r.Header.Get("Idempotency-Key"),
	})
	if errors.Is(err, service.ErrValidation) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "failed to create job")
		return
	}

	writeJSON(w, http.StatusCreated, toJobResponse(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.GetJob(r.Context(), r.PathValue("id"))
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "failed to load job")
		return
	}
	if !s.checkTeamAccess(r, job.Team) {
		writeError(w, http.StatusForbidden, "token team does not match job team")
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(job))
}

type jobPage struct {
	Jobs       []jobResponse `json:"jobs"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit := parseIntQuery(r, "limit", service.DefaultPageLimit)
	team, _ := authenticatedTeam(r.Context()) // empty (all teams) if auth is disabled
	jobs, next, err := s.Jobs.ListJobs(r.Context(), team, r.URL.Query().Get("cursor"), limit)
	if errors.Is(err, service.ErrValidation) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "failed to list jobs")
		return
	}

	resp := jobPage{Jobs: make([]jobResponse, len(jobs)), NextCursor: next}
	for i, j := range jobs {
		resp.Jobs[i] = toJobResponse(j)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Authorize before deleting: checkTeamAccess needs to know the job's
	// team, and that must be established before anything is torn down.
	if len(s.JWTSecret) > 0 {
		job, err := s.Jobs.GetJob(r.Context(), id)
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		if err != nil {
			s.handleServiceError(w, err, "failed to load job")
			return
		}
		if !s.checkTeamAccess(r, job.Team) {
			writeError(w, http.StatusForbidden, "token team does not match job team")
			return
		}
	}

	err := s.Jobs.DeleteJob(r.Context(), id)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "failed to delete job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type dlqEntryResponse struct {
	Submission any    `json:"submission,omitempty"`
	Raw        string `json:"raw,omitempty"`
}

func (s *Server) handleListDLQ(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Jobs.ListDLQ(r.Context(), 100)
	if err != nil {
		s.handleServiceError(w, err, "failed to read dead-letter queue")
		return
	}

	out := make([]dlqEntryResponse, len(entries))
	for i, e := range entries {
		resp := dlqEntryResponse{Raw: e.Raw}
		if e.Submission != nil {
			// Assigning a nil *T into an `any` field yields a non-nil interface
			// (type set, value nil), which defeats omitempty — only set it here.
			resp.Submission = e.Submission
		}
		out[i] = resp
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

type capacityResponse struct {
	Total     int64 `json:"total"`
	Available int64 `json:"available"`
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	cap, err := s.Jobs.Capacity(r.Context())
	if err != nil {
		s.handleServiceError(w, err, "failed to compute capacity")
		return
	}
	writeJSON(w, http.StatusOK, capacityResponse{Total: cap.Total, Available: cap.Available})
}

type auditEntryResponse struct {
	JobID     string `json:"jobId"`
	Team      string `json:"team"`
	Action    string `json:"action"`
	CreatedAt string `json:"createdAt"`
}

type auditPage struct {
	Entries    []auditEntryResponse `json:"entries"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := parseIntQuery(r, "limit", service.DefaultPageLimit)
	entries, next, err := s.Jobs.ListAudit(r.Context(), r.URL.Query().Get("cursor"), limit)
	if errors.Is(err, service.ErrValidation) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.handleServiceError(w, err, "failed to list audit log")
		return
	}

	resp := auditPage{Entries: make([]auditEntryResponse, len(entries)), NextCursor: next}
	for i, e := range entries {
		resp.Entries[i] = auditEntryResponse{
			JobID: e.JobID, Team: e.Team, Action: e.Action, CreatedAt: e.CreatedAt.Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleServiceError(w http.ResponseWriter, err error, msg string) {
	s.Logger.Error(msg, "error", err)
	writeError(w, http.StatusInternalServerError, msg)
}

func parseIntQuery(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
