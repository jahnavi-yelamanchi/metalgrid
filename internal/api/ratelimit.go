package api

import (
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// perTeamLimiter hands out one token-bucket limiter per team, so one
// tenant's burst can't starve another's request budget.
type perTeamLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

func newPerTeamLimiter(rps float64, burst int) *perTeamLimiter {
	return &perTeamLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

func (l *perTeamLimiter) allow(team string) bool {
	l.mu.Lock()
	lim, ok := l.limiters[team]
	if !ok {
		lim = rate.NewLimiter(l.rps, l.burst)
		l.limiters[team] = lim
	}
	l.mu.Unlock()
	return lim.Allow()
}

// RateLimitMiddleware caps requests per team (as established by
// AuthMiddleware, which must run first) and returns 429 once a team's
// burst is exhausted.
func (l *perTeamLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team, ok := authenticatedTeam(r.Context())
		if !ok {
			// AuthMiddleware didn't run or rejected the request already.
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(team) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded for team "+team)
			return
		}
		next.ServeHTTP(w, r)
	})
}
