// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"fmt"
	"sync"
	"testing"
)

func testRoute(label, ip string, port int) Route {
	return Route{ServiceID: "sid-" + label, Label: label, PeerIP: ip, TargetPort: port}
}

func TestTable_LookupNormalizesHost(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{testRoute("app", "100.64.0.5", 47800)})
	for _, host := range []string{
		"app.svc.example.test", "APP.svc.Example.test", "app.svc.example.test:443",
		"app.svc.example.test.", "App.Svc.Example.Test.:8443", " app.svc.example.test ",
	} {
		e, ok := tb.Lookup(host)
		if !ok || e.Route.Upstream() != "100.64.0.5:47800" {
			t.Errorf("Lookup(%q) = %v, %v", host, e, ok)
		}
	}
	for _, host := range []string{
		"other.svc.example.test", "app.example.test", "svc.example.test", "",
		"x.app.svc.example.test", "app.svc.example.test..", ".app.svc.example.test",
		"app.svc.example.test:", "app.svc.example.test:443:443", "xapp.svc.example.test",
	} {
		if _, ok := tb.Lookup(host); ok {
			t.Errorf("Lookup(%q) matched", host)
		}
	}
}

func TestTable_UppercaseSuffixAndLabelKeys(t *testing.T) {
	tb := NewTable("SVC.Example.Test.")
	tb.Replace([]Route{testRoute("app", "100.64.0.5", 1)})
	if _, ok := tb.Lookup("app.svc.example.test"); !ok {
		t.Error("suffix must be normalised to lowercase without a trailing dot")
	}
}

func TestTable_ReplaceSkipsInvalidRoutes(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{
		testRoute("ok", "100.64.0.5", 47800),
		testRoute("badip", "nope", 47800),
		testRoute("badport", "100.64.0.6", 0),
		testRoute("Bad.Label", "100.64.0.7", 47800),
	})
	if tb.Len() != 1 {
		t.Errorf("Len = %d, want 1", tb.Len())
	}
}

func TestTable_ReplaceCancelsRemovedAndChangedOnly(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{testRoute("keep", "100.64.0.5", 1), testRoute("move", "100.64.0.6", 2), testRoute("gone", "100.64.0.7", 3)})
	keep, _ := tb.Lookup("keep.svc.example.test")
	move, _ := tb.Lookup("move.svc.example.test")
	gone, _ := tb.Lookup("gone.svc.example.test")

	tb.Replace([]Route{testRoute("keep", "100.64.0.5", 1), testRoute("move", "100.64.0.6", 9)})

	if keep.Context().Err() != nil {
		t.Error("unchanged route must keep its context")
	}
	if move.Context().Err() == nil || gone.Context().Err() == nil {
		t.Error("changed and removed routes must be cancelled")
	}
	if again, _ := tb.Lookup("keep.svc.example.test"); again != keep {
		t.Error("unchanged route must keep its entry")
	}
}

// Readers must only ever see a complete table: every generation holds the
// same N labels, so a half-built table would show fewer.
func TestTable_ConcurrentReplaceIsAtomic(t *testing.T) {
	const n = 50
	gen := func(port int) []Route {
		rs := make([]Route, 0, n)
		for i := 0; i < n; i++ {
			rs = append(rs, testRoute(fmt.Sprintf("s%d", i), "100.64.0.5", port))
		}
		return rs
	}
	tb := NewTable("svc.example.test")
	tb.Replace(gen(1))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := tb.Len(); got != n {
					t.Errorf("reader saw %d routes, want %d", got, n)
					return
				}
				if _, ok := tb.Lookup("s0.svc.example.test"); !ok {
					t.Error("reader lost a route mid-swap")
					return
				}
			}
		}()
	}
	for i := 0; i < 500; i++ {
		tb.Replace(gen(1 + i%2))
	}
	close(stop)
	wg.Wait()
}

func TestTable_SuffixIsNormalized(t *testing.T) {
	if got := NewTable(" SVC.Example.Test. ").Suffix(); got != "svc.example.test" {
		t.Errorf("Suffix = %q", got)
	}
}
