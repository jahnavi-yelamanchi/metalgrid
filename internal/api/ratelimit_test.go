package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPerTeamLimiterAllowsUpToBurst(t *testing.T) {
	l := newPerTeamLimiter(1, 3)
	for i := 0; i < 3; i++ {
		if !l.allow("team-a") {
			t.Fatalf("expected request %d within burst to be allowed", i)
		}
	}
	if l.allow("team-a") {
		t.Error("expected request beyond burst to be denied")
	}
}

func TestPerTeamLimiterIsolatesTeams(t *testing.T) {
	l := newPerTeamLimiter(1, 1)
	if !l.allow("team-a") {
		t.Fatal("expected first team-a request to be allowed")
	}
	if l.allow("team-a") {
		t.Fatal("expected second team-a request to be denied (burst exhausted)")
	}
	if !l.allow("team-b") {
		t.Error("team-b should have its own budget, unaffected by team-a's usage")
	}
}

func TestRateLimitMiddlewareRejectsOverBurst(t *testing.T) {
	l := newPerTeamLimiter(1, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := l.Middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req = req.WithContext(context.WithValue(req.Context(), teamContextKey, "team-a"))

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("expected second request to be rate limited (429), got %d", rec2.Code)
	}
}

func TestRateLimitMiddlewareSkipsUnauthenticatedRequests(t *testing.T) {
	l := newPerTeamLimiter(1, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := l.Middleware(next)

	// No team in context (as if auth were disabled) — middleware should pass through, not panic or block.
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected pass-through when no authenticated team is present, got %d", rec.Code)
	}
}
