// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// HTTPServer hosts the OIDC redirect / callback routes plus a thin
// JSON REST bridge under /api/v1/* that the Web UI consumes. The gRPC
// surface remains the canonical API; REST is a SSR-friendly read-only
// projection in Phase 1.
type HTTPServer struct {
	addr      string
	srv       *http.Server
	providers map[string]auth.OIDCProvider
	tenants   *repo.Tenants
	users     *repo.Users
	peers     *repo.Peers
	policies  *repo.Policies
	traces    *clickhouse.Traces
	secret    []byte
	baseURL   string
	ttl       time.Duration
}

// NewHTTPServer constructs the OIDC + REST HTTP frontend. ch may be nil
// (REST recommendation endpoints degrade gracefully).
func NewHTTPServer(addr string, pool *db.Pool, providers map[string]auth.OIDCProvider, ch *clickhouse.Conn, secret []byte, baseURL string, ttl time.Duration) *HTTPServer {
	mux := http.NewServeMux()
	h := &HTTPServer{
		addr:      addr,
		providers: providers,
		tenants:   repo.NewTenants(pool),
		users:     repo.NewUsers(pool),
		peers:     repo.NewPeers(pool),
		policies:  repo.NewPolicies(pool),
		traces:    clickhouse.NewTraces(ch),
		secret:    secret,
		baseURL:   baseURL,
		ttl:       ttl,
	}
	mux.HandleFunc("/auth/", h.routeAuth)
	mux.HandleFunc("/api/v1/", h.routeAPI)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h.srv = &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return h
}

// withCORS adds permissive CORS for the dev environment so the Next.js
// app can call the controller from the browser when running outside of
// SSR. Production should narrow Origin via configuration.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-Slug")
		w.Header().Set("Access-Control-Max-Age", "300")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run blocks until ctx is canceled or the listener errors.
func (h *HTTPServer) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", h.addr)
		err := h.srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// routeAuth dispatches `/auth/{provider}/{action}`.
//
// Supported routes:
//
//	/auth/{provider}/login?tenant={slug}    redirect to provider for consent
//	/auth/{provider}/callback?code=&state=  finish the flow, mint session JWT
func (h *HTTPServer) routeAuth(w http.ResponseWriter, r *http.Request) {
	// path layout: /auth/{provider}/{action}
	parts := splitPath(r.URL.Path)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	providerName := parts[1]
	action := parts[2]
	provider, ok := h.providers[providerName]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	switch action {
	case "login":
		h.handleLogin(w, r, provider)
	case "callback":
		h.handleCallback(w, r, provider)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPServer) handleLogin(w http.ResponseWriter, r *http.Request, provider auth.OIDCProvider) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = "default"
	}
	state, err := auth.IssueOIDCState(h.secret, tenant, 10*time.Minute)
	if err != nil {
		http.Error(w, "issue state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirect := fmt.Sprintf("%s/auth/%s/callback", h.baseURL, provider.Name())
	http.Redirect(w, r, provider.AuthURL(state, redirect), http.StatusFound)
}

func (h *HTTPServer) handleCallback(w http.ResponseWriter, r *http.Request, provider auth.OIDCProvider) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	tenantSlug, err := auth.VerifyOIDCState(h.secret, state)
	if err != nil {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	redirect := fmt.Sprintf("%s/auth/%s/callback", h.baseURL, provider.Name())
	identity, err := provider.Exchange(r.Context(), code, redirect)
	if err != nil {
		http.Error(w, "exchange: "+err.Error(), http.StatusBadGateway)
		return
	}

	tenant, err := h.tenants.GetOrCreate(r.Context(), tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		http.Error(w, "tenant: "+err.Error(), http.StatusInternalServerError)
		return
	}

	user, err := h.users.UpsertOIDC(r.Context(), &repo.User{
		TenantID:     tenant.ID,
		Email:        identity.Email,
		DisplayName:  identity.DisplayName,
		OIDCProvider: identity.Provider,
		OIDCSubject:  identity.Subject,
	})
	if err != nil {
		http.Error(w, "upsert user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token, err := auth.IssueSessionToken(h.secret, auth.SessionClaims{
		UserID:   user.ID,
		TenantID: tenant.ID,
	}, h.ttl)
	if err != nil {
		http.Error(w, "issue token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>bamboo session issued</title>
<style>body{font-family:system-ui;max-width:40rem;margin:3rem auto;padding:1rem}code{background:#f4f4f6;padding:.5rem;display:block;word-break:break-all}</style>
<h1>Logged in as %s</h1>
<p>Tenant: <strong>%s</strong></p>
<p>Bearer token (paste into your client):</p>
<code>%s</code>
`, htmlEscape(identity.Email), htmlEscape(tenant.Slug), htmlEscape(token))
}

// splitPath splits a URL path into segments without leading or trailing
// empties. "/auth/google/login" → ["auth","google","login"].
func splitPath(p string) []string {
	var out []string
	current := ""
	for _, r := range p {
		if r == '/' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// htmlEscape escapes a string for safe inclusion in HTML.
func htmlEscape(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for _, c := range r {
		switch c {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		case '\'':
			out = append(out, []rune("&#39;")...)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
