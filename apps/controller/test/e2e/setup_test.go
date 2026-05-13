// SPDX-License-Identifier: AGPL-3.0-or-later

// Package e2e drives the controller through gRPC against a real Postgres.
//
// Tests are skipped when DATABASE_URL_TEST is unset or when -short is used.
// The CI workflow sets DATABASE_URL_TEST to a Postgres service container.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/server"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// fixture bundles a running gRPC server and the connection details tests
// need to drive it. The HTTP fixture (httpURL) is wired against the same
// CoordinatorHandler so /api/v1/peers/* and the SSE stream share state
// with the gRPC handlers.
type fixture struct {
	addr       string
	pool       *db.Pool
	grpc       *grpc.Server
	conn       *grpc.ClientConn
	auth       bamboov1.AuthServiceClient
	coord      bamboov1.CoordinatorServiceClient
	policy     bamboov1.PolicyServiceClient
	tenantSlug string // unique per test, prevents cross-test interference
	httpURL    string // base URL of the in-process HTTP fixture
	httpSrv    *httptest.Server
	httpAPI    *server.HTTPServer            // for per-test config knobs (e.g. SetRequireAuth)
	coordSrv   *handlers.CoordinatorHandler  // server-side handler, for SetRequireAuth in tests
}

// startFixture brings up an in-process controller against a real Postgres
// and returns a client wired to it. The fixture cleans itself up via
// t.Cleanup.
func startFixture(t *testing.T) *fixture {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping e2e test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		pool.Close()
		t.Fatalf("listen: %v", err)
	}

	grpcSrv, coord := buildGRPCFixture(pool)

	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		grpcSrv.Stop()
		pool.Close()
		t.Fatalf("dial: %v", err)
	}

	handler, httpAPI := buildHTTPMux(pool, coord)
	httpSrv := httptest.NewServer(handler)

	f := &fixture{
		addr:       lis.Addr().String(),
		pool:       pool,
		grpc:       grpcSrv,
		conn:       conn,
		auth:       bamboov1.NewAuthServiceClient(conn),
		coord:      bamboov1.NewCoordinatorServiceClient(conn),
		policy:     bamboov1.NewPolicyServiceClient(conn),
		tenantSlug: fmt.Sprintf("e2e-%s", uuid.NewString()[:8]),
		httpURL:    httpSrv.URL,
		httpSrv:    httpSrv,
		httpAPI:    httpAPI,
		coordSrv:   coord,
	}

	t.Cleanup(func() {
		_ = conn.Close()
		httpSrv.Close()
		grpcSrv.GracefulStop()
		// Best-effort cleanup of rows this test created.
		cleanupTenant(pool, f.tenantSlug)
		pool.Close()
	})

	return f
}

// buildGRPCFixture is a test-only constructor that mirrors what
// server.New does in production but exposes the CoordinatorHandler so
// the HTTP fixture can share it. The AuthHandler is wired with the
// fixture's known session secret so RequireAdmin can verify test JWTs
// minted via f.mintJWT.
func buildGRPCFixture(pool *db.Pool) (*grpc.Server, *handlers.CoordinatorHandler) {
	bus := events.NewBus()
	s := grpc.NewServer()
	authHandler := handlers.NewAuthHandler(pool)
	authHandler.SetOIDCConfig("http://127.0.0.1", []byte("e2e-secret-with-at-least-32-bytes-padding"), 1*time.Hour)
	coord := handlers.NewCoordinatorHandler(pool, authHandler, bus)
	bamboov1.RegisterAuthServiceServer(s, authHandler)
	bamboov1.RegisterCoordinatorServiceServer(s, coord)
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler(pool, nil, bus, authHandler))
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, nil))
	return s, coord
}

// buildHTTPMux returns the controller's HTTP routes (auth + REST API)
// wired against the same coordinator handler the gRPC server uses.
// The *server.HTTPServer is exposed so per-test config knobs (e.g.
// SetRequireAuth) can be toggled without reconstructing the fixture.
func buildHTTPMux(pool *db.Pool, coord *handlers.CoordinatorHandler) (http.Handler, *server.HTTPServer) {
	secret := []byte("e2e-secret-with-at-least-32-bytes-padding")
	httpSrv := server.NewHTTPServer(
		"127.0.0.1:0",
		pool,
		map[string]auth.OIDCProvider{},
		nil, // no clickhouse in tests
		secret,
		"http://127.0.0.1",
		1*time.Hour,
		coord,
	)
	return httpSrv.Handler(), httpSrv
}

// enableRequireAuth flips the fixture into prod-mode auth so the next
// HTTP request that lacks a credential (and isn't on the /api/v1/me
// allowlist) returns 401. Also flips the CoordinatorHandler so the
// REST register adapter rejects slug-only callers — both knobs read
// from the same env var in production wiring.
func (f *fixture) enableRequireAuth() {
	f.httpAPI.SetRequireAuth(true)
	f.coordSrv.SetRequireAuth(true)
}

// mintJWT creates a user in the fixture's tenant and returns a session
// token signed with the fixture's known secret. Used by tests that need
// to exercise authenticated REST flows under SetRequireAuth.
func (f *fixture) mintJWT(t *testing.T, isAdmin bool) string {
	t.Helper()
	tok, _ := f.mintJWTWithUser(t, isAdmin)
	return tok
}

// mintJWTWithUser returns both the signed token and the underlying
// user.ID so tests that need to mutate the user (e.g. move it to a
// different tenant to exercise the membership-mismatch path) can do
// so without ambiguous SQL.
func (f *fixture) mintJWTWithUser(t *testing.T, isAdmin bool) (string, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenants := repo.NewTenants(f.pool)
	tenant, err := tenants.GetOrCreate(ctx, f.tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	users := repo.NewUsers(f.pool)
	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        fmt.Sprintf("test-%s@example.com", uuid.NewString()[:8]),
		DisplayName:  "Test User",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("UpsertOIDC: %v", err)
	}
	if isAdmin {
		if _, err := f.pool.Exec(ctx, `UPDATE users SET is_admin=true WHERE id=$1`, user.ID); err != nil {
			t.Fatalf("set is_admin: %v", err)
		}
	}
	tok, err := auth.IssueSessionToken(
		[]byte("e2e-secret-with-at-least-32-bytes-padding"),
		auth.SessionClaims{UserID: user.ID, TenantID: tenant.ID},
		1*time.Hour,
	)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}
	return tok, user.ID
}

// outgoingCtx returns a ctx that carries the fixture's tenant slug as gRPC
// metadata, so the controller's tenant-resolution fallback isolates each
// test in its own tenant row.
func (f *fixture) outgoingCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-tenant-slug", f.tenantSlug)
}

// randomPubKey returns a base64-encoded 32-byte string suitable for the
// Curve25519 public key field. Tests do not actually establish WireGuard
// tunnels, so the key need only be unique and well-formed.
func randomPubKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// cleanupTenant removes every row created by a given test tenant. Errors
// are intentionally ignored — best-effort cleanup must not fail the test.
func cleanupTenant(pool *db.Pool, slug string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE slug = $1`, slug)
}
