package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// teamClaims is what MetalGrid expects in a submitted JWT. In production
// this would be an OIDC ID token verified against a JWKS endpoint; for this
// project's $0 local-dev environment, a shared-secret HS256 token gives the
// same request-time contract (verified team claim) without standing up an
// issuer.
// ponytail: HS256 + shared secret, not full OIDC. Upgrade path: swap
// jwt.ParseWithClaims's key func for a JWKS-backed one (e.g. dexidp/dex)
// when there's a real identity provider to trust.
type teamClaims struct {
	Team string `json:"team"`
	jwt.RegisteredClaims
}

type contextKey string

const teamContextKey contextKey = "metalgrid-team"

// AuthMiddleware validates the Authorization: Bearer <token> header and
// stashes the token's team claim in the request context.
func AuthMiddleware(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team, err := authenticate(r, secret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or missing token: "+err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), teamContextKey, team)))
	})
}

func authenticate(r *http.Request, secret []byte) (string, error) {
	header := r.Header.Get("Authorization")
	tokenStr, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || tokenStr == "" {
		return "", errMissingToken
	}

	claims := &teamClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return "", err
	}
	if claims.Team == "" {
		return "", errMissingTeamClaim
	}
	return claims.Team, nil
}

var (
	errMissingToken     = jwtErr("missing bearer token")
	errMissingTeamClaim = jwtErr("token has no team claim")
)

type jwtErr string

func (e jwtErr) Error() string { return string(e) }

// authenticatedTeam reads the team stashed by AuthMiddleware.
func authenticatedTeam(ctx context.Context) (string, bool) {
	team, ok := ctx.Value(teamContextKey).(string)
	return team, ok
}
