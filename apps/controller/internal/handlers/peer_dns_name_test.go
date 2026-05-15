// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSlugifyHostname(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "mac-mini", "mac-mini"},
		{"mixed-case", "MacBook-Pro", "macbook-pro"},
		{"spaces", "Mac mini of Han", "mac-mini-of-han"},
		{"punctuation", "Adam's iPhone 15!", "adam-s-iphone-15"},
		{"underscores", "k8s_worker_03", "k8s-worker-03"},
		{"consecutive-disallowed", "foo!!!bar", "foo-bar"},
		{"trailing-punct", "foo!!!", "foo"},
		{"leading-punct", "!!!foo", "foo"},
		{"only-disallowed", "!@#$%", ""},
		{"non-ascii-only", "黃柏漢的mac", "mac"},
		{"all-non-ascii", "黃柏漢", ""},
		{"emoji", "💻 dev box", "dev-box"},
		{"already-clean", "abc-123", "abc-123"},
		{"empty", "", ""},
		{"long", strings.Repeat("a", 100), strings.Repeat("a", 63)},
		// 64 chars of `a` followed by `-bbb`: truncation at 63 cuts mid-
		// way through the `a` run, no trailing dash to trim.
		{"long-no-trailing-dash", strings.Repeat("a", 64) + "bbb", strings.Repeat("a", 63)},
		// When truncation lands on a dash boundary it should be trimmed.
		// 62 'a's + dash + tail → 63 chars including the dash; trim it.
		{"long-trim-trailing-dash", strings.Repeat("a", 62) + "-tail", strings.Repeat("a", 62)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SlugifyHostname(tc.in)
			if got != tc.want {
				t.Errorf("SlugifyHostname(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// stubResolver is an in-memory peerDNSResolver used by the resolver
// tests. Set `err` to drive the failure path; the `taken` set is the
// list of dns names already claimed by other peers in the tenant.
type stubResolver struct {
	taken map[string]bool
	err   error
}

func (s *stubResolver) IsDNSNameTaken(_ context.Context, _ uuid.UUID, name string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.taken[name], nil
}

func TestResolveDNSName_FirstChoiceFree(t *testing.T) {
	r := &stubResolver{taken: map[string]bool{}}
	got, err := ResolveDNSName(context.Background(), r, uuid.New(), "macmini")
	if err != nil {
		t.Fatalf("ResolveDNSName: %v", err)
	}
	if got != "macmini" {
		t.Errorf("got %q, want %q", got, "macmini")
	}
}

func TestResolveDNSName_CollisionAppendsSuffix(t *testing.T) {
	r := &stubResolver{taken: map[string]bool{
		"macmini":   true,
		"macmini-2": true,
	}}
	got, err := ResolveDNSName(context.Background(), r, uuid.New(), "macmini")
	if err != nil {
		t.Fatalf("ResolveDNSName: %v", err)
	}
	if got != "macmini-3" {
		t.Errorf("got %q, want %q", got, "macmini-3")
	}
}

func TestResolveDNSName_EmptyBaseFallsBackToPeer(t *testing.T) {
	r := &stubResolver{taken: map[string]bool{"peer": true}}
	got, err := ResolveDNSName(context.Background(), r, uuid.New(), "")
	if err != nil {
		t.Fatalf("ResolveDNSName: %v", err)
	}
	if got != "peer-2" {
		t.Errorf("got %q, want %q", got, "peer-2")
	}
}

func TestResolveDNSName_TruncatesForSuffix(t *testing.T) {
	// Base at the 63-char limit means the suffix would push us over;
	// the resolver should trim the base before appending.
	long := strings.Repeat("a", maxDNSLabelLen)
	r := &stubResolver{taken: map[string]bool{long: true}}
	got, err := ResolveDNSName(context.Background(), r, uuid.New(), long)
	if err != nil {
		t.Fatalf("ResolveDNSName: %v", err)
	}
	if len(got) > maxDNSLabelLen {
		t.Errorf("ResolveDNSName returned %d-char name; want <= %d", len(got), maxDNSLabelLen)
	}
	if !strings.HasSuffix(got, "-2") {
		t.Errorf("expected -2 suffix on first collision, got %q", got)
	}
}

func TestResolveDNSName_RepoError(t *testing.T) {
	want := errors.New("db down")
	r := &stubResolver{err: want}
	if _, err := ResolveDNSName(context.Background(), r, uuid.New(), "x"); err == nil {
		t.Fatal("expected error from underlying repo, got nil")
	}
}

func TestResolveDNSName_Exhausted(t *testing.T) {
	// Mark candidates 0..99 all taken; resolver must give up rather
	// than loop forever.
	taken := map[string]bool{"x": true}
	for i := 1; i < 100; i++ {
		taken[candidateWithSuffix("x", i)] = true
	}
	r := &stubResolver{taken: taken}
	if _, err := ResolveDNSName(context.Background(), r, uuid.New(), "x"); err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}
