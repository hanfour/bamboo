// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/ratelimit"
)

// rateLimiters bundles the per-surface limiters. Each is keyed by client
// IP inside the middleware, so one abusive IP can't lock others out.
// Limits are generous enough for legitimate shared-NAT bursts (many
// peers behind one public IP) while still stopping unbounded brute force.
type rateLimiters struct {
	auth       *ratelimit.Limiter // OIDC login/callback
	register   *ratelimit.Limiter // pre-auth-key redemption
	relayToken *ratelimit.Limiter // relay-token minting
}

// forPath returns the limiter + category for a rate-limited surface, or
// (nil, "") when the path isn't limited. /auth/sign-out is excluded — it
// only clears a cookie and isn't a guessing target.
func (rl *rateLimiters) forPath(path string) (*ratelimit.Limiter, string) {
	switch {
	case path == "/api/v1/peers/register":
		return rl.register, "register"
	case path == "/api/v1/relay-token":
		return rl.relayToken, "relay-token"
	case strings.HasPrefix(path, "/auth/") && path != "/auth/sign-out":
		return rl.auth, "auth"
	}
	return nil, ""
}

// SetRateLimiting enables or disables per-IP brute-force limiting on the
// login / register / relay-token endpoints (audit H-4). Defaults built
// here are deliberately generous so a busy tenant behind one NAT isn't
// throttled; they still cap a brute-force attempt to a bounded rate. The
// e2e fixture never calls this, so tests run unthrottled by default.
func (h *HTTPServer) SetRateLimiting(enabled bool) {
	if !enabled {
		h.rateLimit = nil
		return
	}
	h.rateLimit = &rateLimiters{
		auth:       ratelimit.New(20, 10),  // 20/min sustained, burst 10
		register:   ratelimit.New(60, 30),  // peers behind a NAT onboard in bursts
		relayToken: ratelimit.New(120, 60), // hourly refresh × many peers per IP
	}
}

// rateLimitMiddleware throttles the brute-force surfaces per client IP.
// A no-op when limiting is disabled (h.rateLimit == nil). On a breach it
// returns 429 with a Retry-After header. Reads h.rateLimit per request so
// SetRateLimiting can be called after NewHTTPServer wires the chain.
func (h *HTTPServer) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl := h.rateLimit; rl != nil {
			if lim, cat := rl.forPath(r.URL.Path); lim != nil {
				key := cat + ":" + requestIPString(r)
				if !lim.Allow(key) {
					secs := int(lim.RetryAfter(key).Seconds())
					if secs < 1 {
						secs = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(secs))
					writeError(w, http.StatusTooManyRequests, errors.New("too many requests; slow down"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// StartRateLimitCleanup periodically evicts idle, refilled buckets so the
// per-IP maps don't grow unbounded under a churn of distinct client IPs
// (incl. spoofed X-Forwarded-For). No-op when limiting is disabled.
func (h *HTTPServer) StartRateLimitCleanup(ctx context.Context) {
	if h.rateLimit == nil {
		return
	}
	rl := h.rateLimit
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				const idle = 15 * time.Minute
				rl.auth.Cleanup(idle)
				rl.register.Cleanup(idle)
				rl.relayToken.Cleanup(idle)
			}
		}
	}()
}
