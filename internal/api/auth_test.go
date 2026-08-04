package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signToken(t *testing.T, secret []byte, team string, expiry time.Duration) string {
	t.Helper()
	claims := teamClaims{
		Team:             team,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry))},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

func TestAuthenticateValidToken(t *testing.T) {
	secret := []byte("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, secret, "platform", time.Hour))

	team, err := authenticate(req, secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if team != "platform" {
		t.Errorf("expected team=platform, got %s", team)
	}
}

func TestAuthenticateMissingHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	if _, err := authenticate(req, []byte("secret")); err == nil {
		t.Error("expected error for missing Authorization header")
	}
}

func TestAuthenticateWrongSecret(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, []byte("right-secret"), "platform", time.Hour))

	if _, err := authenticate(req, []byte("wrong-secret")); err == nil {
		t.Error("expected error for token signed with a different secret")
	}
}

func TestAuthenticateExpiredToken(t *testing.T) {
	secret := []byte("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, secret, "platform", -time.Hour))

	if _, err := authenticate(req, secret); err == nil {
		t.Error("expected error for expired token")
	}
}

func TestAuthenticateMissingTeamClaim(t *testing.T) {
	secret := []byte("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, secret, "", time.Hour))

	if _, err := authenticate(req, secret); err == nil {
		t.Error("expected error for token with no team claim")
	}
}

func TestAuthMiddlewareRejectsUnauthenticated(t *testing.T) {
	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlerCalled = true })

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rec := httptest.NewRecorder()
	AuthMiddleware([]byte("secret"), next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if handlerCalled {
		t.Error("handler should not run for an unauthenticated request")
	}
}

func TestAuthMiddlewarePassesTeamToContext(t *testing.T) {
	secret := []byte("test-secret")
	var gotTeam string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTeam, _ = authenticatedTeam(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, secret, "platform", time.Hour))
	rec := httptest.NewRecorder()
	AuthMiddleware(secret, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotTeam != "platform" {
		t.Errorf("expected team=platform in context, got %s", gotTeam)
	}
}

func TestCheckTeamAccessDisabledWhenNoSecret(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	if !s.checkTeamAccess(req, "any-team") {
		t.Error("expected access allowed when auth is disabled (no JWTSecret configured)")
	}
}

func TestCheckTeamAccessMismatch(t *testing.T) {
	s := &Server{JWTSecret: []byte("secret")}
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	// Simulate AuthMiddleware having already run and stashed the team.
	req = req.WithContext(context.WithValue(req.Context(), teamContextKey, "team-a"))

	if !s.checkTeamAccess(req, "team-a") {
		t.Error("expected access allowed for matching team")
	}
	if s.checkTeamAccess(req, "team-b") {
		t.Error("expected access denied for mismatched team")
	}
}
