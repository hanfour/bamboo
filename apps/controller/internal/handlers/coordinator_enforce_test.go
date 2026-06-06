// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// mustParse is a local helper because policy_test.mustParse is not
// exported.
func mustParsePolicy(t *testing.T, src string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse("test.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestAllowedIPsFor_NilPolicyReturnsTunnelIP(t *testing.T) {
	src := &repo.Peer{ID: uuid.New(), IP: "100.64.0.1", Tags: []string{"dev"}}
	dst := &repo.Peer{ID: uuid.New(), IP: "100.64.0.2", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst, nat64EgressRoute{})
	want := []string{"100.64.0.2/32"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v (full-mesh fallback when no policy)", got, want)
	}
}

func TestAllowedIPsFor_AllowedPair(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := &repo.Peer{IP: "100.64.0.1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "100.64.0.5", Tags: []string{"db"}}
	got := allowedIPsFor(p, src, dst, nat64EgressRoute{})
	if len(got) != 1 || got[0] != "100.64.0.5/32" {
		t.Errorf("got %v, want [100.64.0.5/32]", got)
	}
}

func TestAllowedIPsFor_DeniedPairReturnsNil(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := &repo.Peer{IP: "100.64.0.1", Tags: []string{"prod"}}
	dst := &repo.Peer{IP: "100.64.0.5", Tags: []string{"db"}}
	if got := allowedIPsFor(p, src, dst, nat64EgressRoute{}); got != nil {
		t.Errorf("expected nil for denied pair, got %v", got)
	}
}

func TestAllowedIPsFor_IPv6Destination(t *testing.T) {
	src := &repo.Peer{IP: "fd00::1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "fd00::2", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst, nat64EgressRoute{})
	if len(got) != 1 || got[0] != "fd00::2/128" {
		t.Errorf("got %v, want [fd00::2/128]", got)
	}
}

func TestPeerView_PopulatesAddrAndTags(t *testing.T) {
	p := &repo.Peer{IP: "100.64.0.42", Tags: []string{"web", "prod"}}
	view := peerView(p)
	if view.IP.String() != "100.64.0.42" {
		t.Errorf("IP = %q, want 100.64.0.42", view.IP.String())
	}
	if len(view.Tags) != 2 || view.Tags[0] != "web" {
		t.Errorf("Tags = %v, want [web prod]", view.Tags)
	}
}

func TestPeerView_InvalidIPLeavesZeroAddr(t *testing.T) {
	p := &repo.Peer{IP: "not-an-ip", Tags: nil}
	view := peerView(p)
	if view.IP.IsValid() {
		t.Errorf("expected zero netip.Addr for invalid input, got %v", view.IP)
	}
}

func TestAllowedIPsFor_DualFamilyTunnel(t *testing.T) {
	src := &repo.Peer{IP: "100.64.0.1", IP6: "fdba:1100::6440:1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "100.64.0.5", IP6: "fdba:1100::6440:5", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst, nat64EgressRoute{})
	want := []string{"100.64.0.5/32", "fdba:1100::6440:5/128"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v (dual-family tunnel baseline)", got, want)
	}
}

func TestAllowedIPsFor_DualFamilyBeforeRoutes(t *testing.T) {
	src := &repo.Peer{IP: "100.64.0.1", IP6: "fdba:1100::6440:1", Tags: []string{"dev"}}
	dst := &repo.Peer{
		IP: "100.64.0.5", IP6: "fdba:1100::6440:5", Tags: []string{"prod"},
		ApprovedRoutes: []string{"10.0.0.0/24"},
	}
	got := allowedIPsFor(nil, src, dst, nat64EgressRoute{})
	want := []string{"100.64.0.5/32", "fdba:1100::6440:5/128", "10.0.0.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q (v6 must precede approved routes)", i, got[i], want[i])
		}
	}
}

func TestAllowedIPsFor_NAT64EgressRoute(t *testing.T) {
	egressID := uuid.New()
	src := &repo.Peer{IP: "100.64.0.1", IP6: "fdba:1100::6440:1"}
	dst := &repo.Peer{ID: egressID, IP: "100.64.0.5", IP6: "fdba:1100::6440:5", NAT64EgressApproved: true}
	rc := nat64EgressRoute{enabled: true, prefix: "64:ff9b::/96", egressID: egressID}
	got := allowedIPsFor(nil, src, dst, rc)
	if len(got) != 3 || got[2] != "64:ff9b::/96" {
		t.Errorf("got %v, want [...,64:ff9b::/96]", got)
	}
}

func TestAllowedIPsFor_NAT64EgressRoute_DisabledMaster(t *testing.T) {
	egressID := uuid.New()
	dst := &repo.Peer{ID: egressID, IP: "100.64.0.5", IP6: "fdba:1100::6440:5", NAT64EgressApproved: true}
	rc := nat64EgressRoute{enabled: false, prefix: "64:ff9b::/96", egressID: egressID}
	got := allowedIPsFor(nil, &repo.Peer{IP: "100.64.0.1"}, dst, rc)
	for _, c := range got {
		if c == "64:ff9b::/96" {
			t.Errorf("dns64 master off but egress route present: %v", got)
		}
	}
}

func TestAllowedIPsFor_NAT64EgressRoute_NotActiveEgress(t *testing.T) {
	dst := &repo.Peer{ID: uuid.New(), IP: "100.64.0.5", IP6: "fdba:1100::6440:5", NAT64EgressApproved: true}
	rc := nat64EgressRoute{enabled: true, prefix: "64:ff9b::/96", egressID: uuid.New()}
	got := allowedIPsFor(nil, &repo.Peer{IP: "100.64.0.1"}, dst, rc)
	for _, c := range got {
		if c == "64:ff9b::/96" {
			t.Errorf("non-active egress got the route: %v", got)
		}
	}
}

func TestComputeNAT64EgressRoute_PicksLowestID(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	tenant := &repo.Tenant{DNS64Enabled: true, NAT64Prefix: ""}
	a := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", LastSeenAt: &fresh}
	b := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", LastSeenAt: &fresh}
	c := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003")}
	rc := computeNAT64EgressRoute(tenant, []*repo.Peer{a, b, c}, now)
	if !rc.enabled || rc.prefix != "64:ff9b::/96" || rc.egressID != b.ID {
		t.Errorf("rc = %+v, want enabled, default prefix, egressID=%s", rc, b.ID)
	}
}

func TestComputeNAT64EgressRoute_SkipsNonLiveEgress(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	tenant := &repo.Tenant{DNS64Enabled: true}
	// Lowest-ID egress is pending; next is disabled; only the third
	// (live) one should be selected — a dead egress must not shadow it.
	pending := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NAT64EgressApproved: true, ApprovalStatus: "pending", Status: "online", LastSeenAt: &fresh}
	disabled := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "disabled", LastSeenAt: &fresh}
	live := &repo.Peer{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", LastSeenAt: &fresh}
	rc := computeNAT64EgressRoute(tenant, []*repo.Peer{pending, disabled, live}, now)
	if rc.egressID != live.ID {
		t.Errorf("egressID = %s, want the live egress %s (pending/disabled must be skipped)", rc.egressID, live.ID)
	}
}

func TestComputeNAT64EgressRoute_NoneApproved(t *testing.T) {
	now := time.Now()
	tenant := &repo.Tenant{DNS64Enabled: true}
	rc := computeNAT64EgressRoute(tenant, []*repo.Peer{{ID: uuid.New()}}, now)
	if rc.egressID != uuid.Nil {
		t.Errorf("egressID = %s, want Nil when none approved", rc.egressID)
	}
}

func TestIsEgressEligible(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-2 * nat64EgressStaleAfter)
	healthy, unhealthy, unknown := strptr("healthy"), strptr("unhealthy"), strptr("unknown")

	cases := []struct {
		name string
		p    *repo.Peer
		want bool
	}{
		{"approved+online+healthy+fresh", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: healthy, LastSeenAt: &fresh}, true},
		{"health NULL = eligible", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: nil, LastSeenAt: &fresh}, true},
		{"unknown = eligible", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: unknown, LastSeenAt: &fresh}, true},
		{"unhealthy = skip", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: unhealthy, LastSeenAt: &fresh}, false},
		{"stale = skip", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: healthy, LastSeenAt: &stale}, false},
		{"never-seen = skip", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: healthy, LastSeenAt: nil}, false},
		{"not approved = skip", &repo.Peer{NAT64EgressApproved: false, ApprovalStatus: "approved", Status: "online", LastSeenAt: &fresh}, false},
		{"pending = skip", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "pending", Status: "online", LastSeenAt: &fresh}, false},
		{"disabled = skip", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "disabled", LastSeenAt: &fresh}, false},
	}
	for _, c := range cases {
		if got := isEgressEligible(c.p, now); got != c.want {
			t.Errorf("%s: isEgressEligible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestComputeNAT64EgressRoute_HealthAware(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	tn := &repo.Tenant{DNS64Enabled: true, NAT64Prefix: ""}
	mk := func(id uuid.UUID, health string) *repo.Peer {
		h := health
		return &repo.Peer{ID: id, NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: &h, LastSeenAt: &fresh}
	}
	lo := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	hi := uuid.MustParse("00000000-0000-0000-0000-0000000000ff")

	if got := computeNAT64EgressRoute(tn, []*repo.Peer{mk(hi, "healthy"), mk(lo, "healthy")}, now); got.egressID != lo {
		t.Errorf("both healthy: egressID = %v, want lo", got.egressID)
	}
	if got := computeNAT64EgressRoute(tn, []*repo.Peer{mk(lo, "unhealthy"), mk(hi, "healthy")}, now); got.egressID != hi {
		t.Errorf("lo unhealthy: egressID = %v, want hi", got.egressID)
	}
	if got := computeNAT64EgressRoute(tn, []*repo.Peer{mk(lo, "unhealthy"), mk(hi, "unhealthy")}, now); got.egressID != uuid.Nil {
		t.Errorf("all unhealthy: egressID = %v, want Nil", got.egressID)
	}
}

func strptr(s string) *string { return &s }

func TestSelectEgress(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	mk := func(id uuid.UUID, health string) *repo.Peer {
		h := health
		return &repo.Peer{ID: id, NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: &h, LastSeenAt: &fresh}
	}
	lo := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	hi := uuid.MustParse("00000000-0000-0000-0000-0000000000ff")

	if got := selectEgress([]*repo.Peer{mk(hi, "healthy"), mk(lo, "healthy")}, now); got != lo {
		t.Errorf("both healthy: %v, want lo", got)
	}
	if got := selectEgress([]*repo.Peer{mk(lo, "unhealthy"), mk(hi, "healthy")}, now); got != hi {
		t.Errorf("lo unhealthy: %v, want hi", got)
	}
	if got := selectEgress([]*repo.Peer{mk(lo, "unhealthy"), mk(hi, "unhealthy")}, now); got != uuid.Nil {
		t.Errorf("all unhealthy: %v, want Nil", got)
	}
}

func TestShouldMarkStale(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-2 * nat64EgressStaleAfter)
	healthy, unhealthy := strptr("healthy"), strptr("unhealthy")

	cases := []struct {
		name string
		p    *repo.Peer
		want bool
	}{
		{"approved+stale+healthy → mark", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: healthy, LastSeenAt: &stale}, true},
		{"approved+stale+NULL → mark", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: nil, LastSeenAt: &stale}, true},
		{"approved+fresh → no", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: healthy, LastSeenAt: &fresh}, false},
		{"already unhealthy → no (don't re-write)", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: unhealthy, LastSeenAt: &stale}, false},
		{"never-seen → no (stays unknown)", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "online", NAT64EgressHealthStatus: nil, LastSeenAt: nil}, false},
		{"not approved → no", &repo.Peer{NAT64EgressApproved: false, ApprovalStatus: "approved", Status: "online", LastSeenAt: &stale}, false},
		{"disabled → no", &repo.Peer{NAT64EgressApproved: true, ApprovalStatus: "approved", Status: "disabled", LastSeenAt: &stale}, false},
	}
	for _, c := range cases {
		if got := shouldMarkStale(c.p, now); got != c.want {
			t.Errorf("%s: shouldMarkStale = %v, want %v", c.name, got, c.want)
		}
	}
}
