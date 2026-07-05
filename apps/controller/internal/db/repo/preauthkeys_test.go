// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestPreAuthKeys_MarkRedeemed_SingleUseIsAtomic is the audit M-3
// regression: a single-use key redeemed concurrently must be consumed
// exactly once. Before the WHERE guard on the increment, two Register
// calls could both pass the "use_count > 0" check and both MarkRedeemed,
// onboarding a second device off one single-use key. Run with -race.
func TestPreAuthKeys_MarkRedeemed_SingleUseIsAtomic(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	keys := repo.NewPreAuthKeys(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slug := fmt.Sprintf("preauth-race-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "preauth race", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	key, err := keys.Create(ctx, &repo.PreAuthKey{
		ID:         uuid.New(),
		TenantID:   tenant.ID,
		SecretHash: "not-verified-by-MarkRedeemed",
		Reusable:   false, // single-use — the whole point
	})
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}

	const goroutines = 24
	var (
		wg       sync.WaitGroup
		consumed int32
		errs     int32
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all at once to maximize contention
			ok, err := keys.MarkRedeemed(ctx, key.ID)
			if err != nil {
				atomic.AddInt32(&errs, 1)
				return
			}
			if ok {
				atomic.AddInt32(&consumed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if errs != 0 {
		t.Fatalf("%d MarkRedeemed calls errored", errs)
	}
	if consumed != 1 {
		t.Fatalf("single-use key consumed %d times, want exactly 1", consumed)
	}

	// The key is now used + revoked; a further redemption is refused.
	if ok, err := keys.MarkRedeemed(ctx, key.ID); err != nil || ok {
		t.Errorf("post-consumption MarkRedeemed = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestPreAuthKeys_MarkRedeemed_ReusableAllowsMany confirms the guard only
// bounds single-use keys — a reusable key keeps consuming.
func TestPreAuthKeys_MarkRedeemed_ReusableAllowsMany(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	keys := repo.NewPreAuthKeys(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("preauth-reuse-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "preauth reuse", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	key, err := keys.Create(ctx, &repo.PreAuthKey{
		ID: uuid.New(), TenantID: tenant.ID, SecretHash: "h", Reusable: true,
	})
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}
	for i := 0; i < 5; i++ {
		ok, err := keys.MarkRedeemed(ctx, key.ID)
		if err != nil || !ok {
			t.Fatalf("reusable redemption %d = (%v, %v), want (true, nil)", i, ok, err)
		}
	}
}
