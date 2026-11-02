// SPDX-License-Identifier: AGPL-3.0-or-later

// Package routes holds bamboo-ingress's Host -> upstream table and the
// poller that refreshes it from the controller.
package routes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// Route is one live public service, as served by the controller's
// GET /internal/v1/ingress/routes.
type Route struct {
	ServiceID  string `json:"service_id"`
	Label      string `json:"label"`
	PeerIP     string `json:"peer_ip"`
	TargetPort int    `json:"target_port"`
}

// Upstream is the mesh address to dial.
func (r Route) Upstream() string { return net.JoinHostPort(r.PeerIP, strconv.Itoa(r.TargetPort)) }

// Entry is a Route plus a context that is cancelled when the route
// leaves the table or its upstream changes, so in-flight streams end
// when a service is disabled.
type Entry struct {
	Route  Route
	ctx    context.Context
	cancel context.CancelFunc
}

// Context is cancelled when this entry is retired.
func (e *Entry) Context() context.Context { return e.ctx }

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Table maps public host names to entries. Safe for concurrent use: the
// whole label map is swapped atomically, so readers see either the old or
// the new table, never a mix.
type Table struct {
	suffix  string
	byLabel atomic.Pointer[map[string]*Entry]
}

// NewTable returns an empty table for hosts "<label>.<suffix>".
func NewTable(suffix string) *Table {
	t := &Table{suffix: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")}
	empty := map[string]*Entry{}
	t.byLabel.Store(&empty)
	return t
}

// Lookup returns the entry for host. Matching is case-insensitive; one
// numeric port and one trailing dot are ignored; exactly one label in
// front of the configured suffix is required.
func (t *Table) Lookup(host string) (*Entry, bool) {
	h, ok := normalizeHost(host)
	if !ok {
		return nil, false
	}
	label, ok := strings.CutSuffix(h, "."+t.suffix)
	if !ok || label == "" || strings.Contains(label, ".") {
		return nil, false
	}
	e, ok := (*t.byLabel.Load())[label]
	return e, ok
}

// normalizeHost lowercases host and strips a numeric port and one
// trailing dot. Hosts that still contain ':' (bracketed IPv6, junk) are
// rejected.
func normalizeHost(host string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		port := h[i+1:]
		if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 || len(port) > 5 || port == "" {
			return "", false
		}
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || strings.ContainsAny(h, ":[]/ ") {
		return "", false
	}
	return h, true
}

// Replace installs rs as the new table. Unchanged routes keep their
// entry (and context); removed or changed ones are cancelled. Invalid
// routes are skipped (the poller rejects such payloads before this).
func (t *Table) Replace(rs []Route) {
	for {
		oldp := t.byLabel.Load()
		old := *oldp
		next := make(map[string]*Entry, len(rs))
		for _, r := range rs {
			if validRoute(r) != nil {
				continue
			}
			if e, ok := old[r.Label]; ok && e.Route == r {
				next[r.Label] = e
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			next[r.Label] = &Entry{Route: r, ctx: ctx, cancel: cancel}
		}
		if !t.byLabel.CompareAndSwap(oldp, &next) {
			// Lost a race with another writer; drop our fresh entries and retry.
			for l, e := range next {
				if o, ok := old[l]; !ok || o != e {
					e.cancel()
				}
			}
			continue
		}
		for l, e := range old {
			if n, ok := next[l]; !ok || n != e {
				e.cancel()
			}
		}
		return
	}
}

// Suffix is the normalized public suffix (lowercase, no trailing dot).
func (t *Table) Suffix() string { return t.suffix }

// Len reports the number of routes.
func (t *Table) Len() int { return len(*t.byLabel.Load()) }

// ValidateRoutes reports the first schema problem in rs, including
// duplicate labels. The poller uses it to reject a whole payload.
func ValidateRoutes(rs []Route) error {
	seen := make(map[string]struct{}, len(rs))
	for i, r := range rs {
		if err := validRoute(r); err != nil {
			return fmt.Errorf("route %d: %w", i, err)
		}
		if _, dup := seen[r.Label]; dup {
			return fmt.Errorf("route %d: duplicate label %q", i, r.Label)
		}
		seen[r.Label] = struct{}{}
	}
	return nil
}

func validRoute(r Route) error {
	switch {
	case r.ServiceID == "":
		return errors.New("empty service_id")
	case !labelRE.MatchString(r.Label):
		return fmt.Errorf("invalid label %q", r.Label)
	case r.TargetPort < 1 || r.TargetPort > 65535:
		return fmt.Errorf("invalid target_port %d", r.TargetPort)
	}
	if _, err := netip.ParseAddr(r.PeerIP); err != nil {
		return fmt.Errorf("invalid peer_ip %q", r.PeerIP)
	}
	return nil
}
