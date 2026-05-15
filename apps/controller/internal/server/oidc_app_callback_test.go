// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"
)

// TestValidateAppCallback_AllowsBambooScheme asserts the happy path:
// bamboo:// is the one scheme registered by the macOS + iOS apps and
// must pass validation; the URI is returned verbatim so /login can
// hand it to IssueOIDCState unchanged.
func TestValidateAppCallback_AllowsBambooScheme(t *testing.T) {
	const want = "bamboo://auth/callback"
	got, err := validateAppCallback(want)
	if err != nil {
		t.Fatalf("validateAppCallback(%q): %v", want, err)
	}
	if got != want {
		t.Errorf("returned %q, want %q", got, want)
	}
}

// TestValidateAppCallback_EmptyOK confirms the browser flow (no
// app_callback query param) is the empty-string contract that
// /handleCallback uses to decide between native redirect vs. HTML
// success page.
func TestValidateAppCallback_EmptyOK(t *testing.T) {
	got, err := validateAppCallback("")
	if err != nil {
		t.Fatalf("validateAppCallback(\"\"): %v", err)
	}
	if got != "" {
		t.Errorf("returned %q, want empty", got)
	}
}

// TestValidateAppCallback_RejectsForeignSchemes is the security-load-
// bearing case: any scheme that isn't on the allow-list must be
// refused so an attacker can't craft a /auth/google/login URL that
// ships a freshly minted JWT to an attacker-controlled deep link.
func TestValidateAppCallback_RejectsForeignSchemes(t *testing.T) {
	bad := []string{
		"https://evil.example/oauth",       // any HTTPS URL
		"http://localhost/oauth",           // localhost too
		"javascript:alert(1)",              // injection-y
		"ssh://attacker.example/x",         // anything-not-allowed
		"bamboo-evil://x",                  // prefix-similar but not a match
		"app://auth/callback",              // wrong scheme
	}
	for _, in := range bad {
		if _, err := validateAppCallback(in); err == nil {
			t.Errorf("validateAppCallback(%q) succeeded; want error", in)
		}
	}
}
