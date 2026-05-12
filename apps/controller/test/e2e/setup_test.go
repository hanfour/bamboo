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

	httpSrv := httptest.NewServer(buildHTTPMux(pool, coord))

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
// the HTTP fixture can share it.
func buildGRPCFixture(pool *db.Pool) (*grpc.Server, *handlers.CoordinatorHandler) {
	bus := events.NewBus()
	s := grpc.NewServer()
	authHandler := handlers.NewAuthHandler(pool)
	coord := handlers.NewCoordinatorHandler(pool, authHandler, bus)
	bamboov1.RegisterAuthServiceServer(s, authHandler)
	bamboov1.RegisterCoordinatorServiceServer(s, coord)
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler(pool, nil, bus))
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, nil))
	return s, coord
}

// buildHTTPMux returns the controller's HTTP routes (auth + REST API)
// wired against the same coordinator handler the gRPC server uses.
func buildHTTPMux(pool *db.Pool, coord *handlers.CoordinatorHandler) http.Handler {
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
	return httpSrv.Handler()
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
