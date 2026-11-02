// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// requireAppDB opens a pool as bamboo_app (RLS applies), the role the
// controller runs as. requireDB's maintenance pool bypasses RLS, so RLS
// assertions must use this one.
func requireAppDB(t *testing.T) *db.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type ingressFixture struct {
	tenantID uuid.UUID
	peerID   uuid.UUID
	peerIP   string
}

// seedIngressTenant creates a tenant with one approved peer, using the
// maintenance pool. ipOctet keeps peer IPs distinct per test.
func seedIngressTenant(t *testing.T, admin *db.Pool, ipOctet int) ingressFixture {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("ingress-%s", uuid.NewString()[:8])
	tenant, err := repo.NewTenants(admin).Create(ctx, "Ingress", slug, fmt.Sprintf("100.64.%d.0/24", ipOctet))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	ip := fmt.Sprintf("100.64.%d.5", ipOctet)
	var peerID uuid.UUID
	if err := admin.QueryRow(ctx, `
		INSERT INTO peers (tenant_id, hostname, wireguard_public_key, ip, approval_status)
		VALUES ($1, 'mac', $2, $3, 'approved') RETURNING id`,
		tenant.ID, uuid.NewString(), ip).Scan(&peerID); err != nil {
		t.Fatalf("insert peer: %v", err)
	}
	return ingressFixture{tenantID: tenant.ID, peerID: peerID, peerIP: ip}
}

func randomLabel() string { return "svc-" + uuid.NewString()[:8] }

func TestValidateIngressLabel(t *testing.T) {
	cases := map[string]error{
		"yeschef":                           nil,
		"a":                                 nil,
		"a-1":                               nil,
		"0123456789012345678901234567890a":  nil,                         // 32 chars
		"0123456789012345678901234567890ab": repo.ErrIngressLabelInvalid, // 33
		"-bad":                              repo.ErrIngressLabelInvalid,
		"bad-":                              repo.ErrIngressLabelInvalid,
		"Bad":                               repo.ErrIngressLabelInvalid,
		"has.dot":                           repo.ErrIngressLabelInvalid,
		"":                                  repo.ErrIngressLabelInvalid,
		"www":                               repo.ErrIngressLabelReserved,
		"ingress":                           repo.ErrIngressLabelReserved,
		"controller":                        repo.ErrIngressLabelReserved,
	}
	for label, want := range cases {
		if got := repo.ValidateIngressLabel(label); !errors.Is(got, want) && got != want {
			t.Errorf("ValidateIngressLabel(%q) = %v, want %v", label, got, want)
		}
	}
	for _, p := range []int{1023, 65536, 0} {
		if !errors.Is(repo.ValidateIngressPort(p), repo.ErrIngressPortInvalid) {
			t.Errorf("port %d accepted", p)
		}
	}
	for _, p := range []int{1024, 47800, 65535} {
		if err := repo.ValidateIngressPort(p); err != nil {
			t.Errorf("port %d rejected: %v", p, err)
		}
	}
}

func TestIngress_CreateApproveEnableIsLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 201)
	label := randomLabel()
	approver := uuid.New()

	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if s.Approved() || s.Enabled || s.Live() {
			t.Errorf("new service should be pending/disabled: %+v", s)
		}
		if s.PeerIP != fx.peerIP || s.PeerHostname != "mac" {
			t.Errorf("peer join wrong: ip=%q host=%q", s.PeerIP, s.PeerHostname)
		}
		if _, err := svcs.Approve(ctx, s.ID, approver); err != nil {
			return err
		}
		if _, err := svcs.SetEnabled(ctx, s.ID, true); err != nil {
			return err
		}
		got, err := svcs.GetByID(ctx, s.ID)
		if err != nil {
			return err
		}
		if !got.Live() || got.ApprovedBy == nil || *got.ApprovedBy != approver || got.ApprovedAt == nil {
			t.Errorf("want live+approved by %s, got %+v", approver, got)
		}
		routes, err := svcs.ListLiveRoutes(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if len(routes) != 1 || routes[0].Label != label || routes[0].PeerIP != fx.peerIP || routes[0].TargetPort != 47800 {
			t.Errorf("live routes = %+v", routes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func TestIngress_SetTargetPortResetsApproval(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 202)
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, randomLabel(), 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		if _, err := svcs.SetEnabled(ctx, s.ID, true); err != nil {
			return err
		}
		if _, err := svcs.SetTargetPort(ctx, s.ID, 47801); err != nil {
			return err
		}
		got, err := svcs.GetByID(ctx, s.ID)
		if err != nil {
			return err
		}
		if got.TargetPort != 47801 || got.Approved() || got.ApprovedAt != nil || got.Live() {
			t.Errorf("port change must reset approval: %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func TestIngress_PeerNotApprovedIsNotLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 203)
	label := randomLabel()
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE peers SET approval_status='pending' WHERE id=$1`, fx.peerID); err != nil {
		t.Fatalf("unapprove peer: %v", err)
	}
	live, err := repo.NewIngressServices(app).LabelLive(ctx, label)
	if err != nil {
		t.Fatalf("LabelLive: %v", err)
	}
	if live {
		t.Error("service on a pending peer must not be live")
	}
}

// TestIngress_LabelUniqueAcrossTenants pins the cross-tenant rule and the
// SECURITY DEFINER lookups: both run on the app pool with no tenant GUC.
func TestIngress_LabelUniqueAcrossTenants(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	a := seedIngressTenant(t, admin, 204)
	b := seedIngressTenant(t, admin, 205)
	label := randomLabel()

	if err := db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		s, err := repo.NewIngressServices(q).Create(ctx, a.tenantID, a.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		svcs := repo.NewIngressServices(q)
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	}); err != nil {
		t.Fatalf("create in A: %v", err)
	}

	putIngressToken(t, admin, a, time.Hour)
	unscoped := repo.NewIngressServices(app)
	taken, err := unscoped.LabelTaken(ctx, label)
	if err != nil || !taken {
		t.Fatalf("LabelTaken = %v, %v; want true from a GUC-less app pool", taken, err)
	}
	live, err := unscoped.LabelLive(ctx, label)
	if err != nil || !live {
		t.Fatalf("LabelLive = %v, %v; want true", live, err)
	}
	if other, _ := unscoped.LabelLive(ctx, randomLabel()); other {
		t.Error("unknown label reported live")
	}

	err = db.WithTenant(ctx, app, b.tenantID, func(q db.Querier) error {
		_, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, label, 47800, nil)
		return err
	})
	if !errors.Is(err, repo.ErrIngressLabelTaken) {
		t.Fatalf("create same label in B: err=%v, want ErrIngressLabelTaken", err)
	}
}

func TestIngress_RLSConfinesToTenant(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	a := seedIngressTenant(t, admin, 206)
	b := seedIngressTenant(t, admin, 207)

	var bService uuid.UUID
	if err := db.WithTenant(ctx, app, b.tenantID, func(q db.Querier) error {
		s, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, randomLabel(), 47800, nil)
		if err == nil {
			bService = s.ID
		}
		return err
	}); err != nil {
		t.Fatalf("create in B: %v", err)
	}

	err := db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		list, err := svcs.ListByTenant(ctx, b.tenantID)
		if err != nil {
			return err
		}
		if len(list) != 0 {
			t.Errorf("tenant A listed %d of B's services", len(list))
		}
		if _, err := svcs.GetByID(ctx, bService); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("tenant A GetByID(B's service) err=%v, want ErrNotFound", err)
		}
		if n, err := svcs.SetEnabled(ctx, bService, true); err != nil || n != 0 {
			t.Errorf("tenant A modified B's row: n=%d err=%v", n, err)
		}
		if n, err := svcs.Delete(ctx, bService); err != nil || n != 0 {
			t.Errorf("tenant A deleted B's row: n=%d err=%v", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx A: %v", err)
	}

	err = db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		_, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, randomLabel(), 47800, nil)
		return err
	})
	if err == nil {
		t.Fatal("WITH CHECK accepted an insert for tenant B under tenant A's GUC")
	}
}

func TestIngressTokens_PutGetReplace(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 208)
	first, second := uuid.New(), uuid.New()
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		toks := repo.NewIngressTokens(q)
		if _, err := toks.Get(ctx, fx.tenantID); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("Get before Put: %v, want ErrNotFound", err)
		}
		for _, jti := range []uuid.UUID{first, second} {
			if err := toks.Put(ctx, repo.IngressToken{TenantID: fx.tenantID, PeerID: fx.peerID, JTI: jti, ExpiresAt: exp}); err != nil {
				return err
			}
		}
		got, err := toks.Get(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if got.JTI != second || got.PeerID != fx.peerID || !got.ExpiresAt.Equal(exp) {
			t.Errorf("Get = %+v, want jti %s", got, second)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// TestIngress_PeerMustBelongToTenant pins the WITH CHECK that ties peer_id
// to the row's tenant, on both tables, for insert and update.
func TestIngress_PeerMustBelongToTenant(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	a := seedIngressTenant(t, admin, 209)
	b := seedIngressTenant(t, admin, 210)

	// Same-tenant control: works.
	var svcID uuid.UUID
	if err := db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		s, err := repo.NewIngressServices(q).Create(ctx, a.tenantID, a.peerID, randomLabel(), 47800, nil)
		if err != nil {
			return err
		}
		svcID = s.ID
		return repo.NewIngressTokens(q).Put(ctx, repo.IngressToken{TenantID: a.tenantID, PeerID: a.peerID, JTI: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)})
	}); err != nil {
		t.Fatalf("same-tenant writes: %v", err)
	}

	failing := map[string]func(q db.Querier) error{
		"service insert": func(q db.Querier) error {
			_, err := repo.NewIngressServices(q).Create(ctx, a.tenantID, b.peerID, randomLabel(), 47800, nil)
			return err
		},
		"service update": func(q db.Querier) error {
			_, err := q.Exec(ctx, `UPDATE ingress_services SET peer_id = $2 WHERE id = $1`, svcID, b.peerID)
			return err
		},
		"token put": func(q db.Querier) error {
			return repo.NewIngressTokens(q).Put(ctx, repo.IngressToken{TenantID: a.tenantID, PeerID: b.peerID, JTI: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)})
		},
	}
	for name, fn := range failing {
		if err := db.WithTenant(ctx, app, a.tenantID, fn); err == nil {
			t.Errorf("%s: cross-tenant peer_id accepted", name)
		}
	}
}

func TestIngress_SoftDeletedTenantIsNotLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 211)
	label := randomLabel()
	if err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tenants SET deleted_at = now() WHERE id = $1`, fx.tenantID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if live, err := repo.NewIngressServices(app).LabelLive(ctx, label); err != nil || live {
		t.Errorf("LabelLive = %v, %v; want false", live, err)
	}
	if err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		routes, err := repo.NewIngressServices(q).ListLiveRoutes(ctx, fx.tenantID)
		if err == nil && len(routes) != 0 {
			t.Errorf("live routes for soft-deleted tenant: %+v", routes)
		}
		return err
	}); err != nil {
		t.Fatalf("routes: %v", err)
	}
}

func TestIngress_ExistsForPeer(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 212)
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		before, err := svcs.ExistsForPeer(ctx, fx.peerID)
		if err != nil {
			return err
		}
		if before {
			t.Error("ExistsForPeer = true before any service")
		}
		if _, err := svcs.Create(ctx, fx.tenantID, fx.peerID, randomLabel(), 47800, nil); err != nil {
			return err
		}
		after, err := svcs.ExistsForPeer(ctx, fx.peerID)
		if err != nil {
			return err
		}
		if !after {
			t.Error("ExistsForPeer = false after a pending service was filed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// putIngressToken stores a current ingress token for fx's tenant that
// expires after ttl (negative: already expired).
func putIngressToken(t *testing.T, admin *db.Pool, fx ingressFixture, ttl time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithTenant(ctx, admin, fx.tenantID, func(q db.Querier) error {
		return repo.NewIngressTokens(q).Put(ctx, repo.IngressToken{
			TenantID: fx.tenantID, PeerID: fx.peerID, JTI: uuid.New(), ExpiresAt: time.Now().Add(ttl),
		})
	}); err != nil {
		t.Fatalf("put ingress token: %v", err)
	}
}

// makeRepoLiveService creates, approves, and enables a service on fx's
// peer and returns its label.
func makeRepoLiveService(t *testing.T, app *db.Pool, fx ingressFixture) string {
	t.Helper()
	ctx := context.Background()
	label := randomLabel()
	if err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	}); err != nil {
		t.Fatalf("make live service: %v", err)
	}
	return label
}

// TestIngress_LabelLiveRequiresValidToken: the on-demand TLS ask only
// answers live for a tenant that holds an unexpired ingress token, so a
// tenant without a deployed ingress cannot spend certificate quota.
func TestIngress_LabelLiveRequiresValidToken(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 212)
	label := makeRepoLiveService(t, app, fx)
	svcs := repo.NewIngressServices(app)

	if live, err := svcs.LabelLive(ctx, label); err != nil || live {
		t.Errorf("no token: LabelLive = %v, %v; want false", live, err)
	}
	putIngressToken(t, admin, fx, -time.Minute)
	if live, err := svcs.LabelLive(ctx, label); err != nil || live {
		t.Errorf("expired token: LabelLive = %v, %v; want false", live, err)
	}
	putIngressToken(t, admin, fx, time.Hour)
	if live, err := svcs.LabelLive(ctx, label); err != nil || !live {
		t.Errorf("valid token: LabelLive = %v, %v; want true", live, err)
	}
}

// TestIngress_DisabledPeerIsNotLive: an admin-disabled peer drops out of
// every mesh view, so its services stop being live everywhere: the TLS
// ask, the routes table, and IngressService.Live.
func TestIngress_DisabledPeerIsNotLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 213)
	label := makeRepoLiveService(t, app, fx)
	putIngressToken(t, admin, fx, time.Hour)
	if live, err := repo.NewIngressServices(app).LabelLive(ctx, label); err != nil || !live {
		t.Fatalf("before disable: LabelLive = %v, %v; want true", live, err)
	}
	if _, err := admin.Exec(ctx, `UPDATE peers SET status = 'disabled' WHERE id = $1`, fx.peerID); err != nil {
		t.Fatalf("disable peer: %v", err)
	}
	if live, err := repo.NewIngressServices(app).LabelLive(ctx, label); err != nil || live {
		t.Errorf("LabelLive = %v, %v; want false", live, err)
	}
	if err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		routes, err := svcs.ListLiveRoutes(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if len(routes) != 0 {
			t.Errorf("live routes on a disabled peer: %+v", routes)
		}
		list, err := svcs.ListByTenant(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if len(list) != 1 || list[0].Live() {
			t.Errorf("ListByTenant Live() on a disabled peer: %+v", list)
		}
		return nil
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
}
