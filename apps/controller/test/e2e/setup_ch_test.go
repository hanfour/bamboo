// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/server"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// chFixture is the CH-backed sibling of `fixture`. Same shape, plus a
// live *clickhouse.Conn so tests that need to read back CH writes
// (notably #138 v2 path-transition timeline) can exercise the
// controller → CH → REST round trip end to end.
//
// Kept separate from the default fixture so the non-CH e2e tests don't
// pay the price of a CH ping at startup — most tests don't touch
// ClickHouse and the fixture passing `nil` for CH is the right
// default. CH-dependent tests opt in via startCHFixture.
type chFixture struct {
	*fixture
	ch *clickhouse.Conn
}

// startCHFixture brings up the same controller stack as startFixture
// but additionally requires CLICKHOUSE_URL_TEST so the HTTPServer is
// wired with a real ClickHouse connection. Tests that hit the
// connection-events read or write paths use this.
//
// Skips when CLICKHOUSE_URL_TEST is unset (CI does not currently spin
// up a CH service container; local developers point this at the
// docker-compose instance).
func startCHFixture(t *testing.T) *chFixture {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping e2e test")
	}
	chDSN := os.Getenv("CLICKHOUSE_URL_TEST")
	if chDSN == "" {
		t.Skip("CLICKHOUSE_URL_TEST not set; skipping CH-backed e2e test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ch, err := clickhouse.Open(ctx, chDSN)
	if err != nil {
		pool.Close()
		t.Fatalf("open clickhouse: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		pool.Close()
		_ = ch.Close()
		t.Fatalf("listen: %v", err)
	}

	grpcSrv, coord := buildGRPCFixture(pool)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		grpcSrv.Stop()
		pool.Close()
		_ = ch.Close()
		t.Fatalf("dial: %v", err)
	}

	handler, httpAPI := buildHTTPMuxWithCH(pool, coord, ch)
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
		cleanupTenant(pool, f.tenantSlug)
		pool.Close()
		_ = ch.Close()
	})

	return &chFixture{fixture: f, ch: ch}
}

// buildHTTPMuxWithCH is the CH-wired companion to buildHTTPMux. Kept
// as a separate constructor so the default e2e fixture stays free of
// the CH connect cost.
func buildHTTPMuxWithCH(pool *db.Pool, coord *handlers.CoordinatorHandler, ch *clickhouse.Conn) (http.Handler, *server.HTTPServer) {
	secret := []byte("e2e-secret-with-at-least-32-bytes-padding")
	httpSrv := server.NewHTTPServer(
		"127.0.0.1:0",
		pool,
		map[string]auth.OIDCProvider{},
		ch,
		secret,
		"http://127.0.0.1",
		1*time.Hour,
		coord,
	)
	httpSrv.StartPublisher(context.Background())
	return httpSrv.Handler(), httpSrv
}
