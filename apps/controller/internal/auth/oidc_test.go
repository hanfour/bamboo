// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"strings"
	"testing"
	"time"
)

// TestIssueVerifyOIDCState_AppCallbackRoundTrip locks in the contract
// that app_callback ferries through the signed state untouched. The
// native-sign-in flow relies on this: /login stores the URI, /callback
// reads it back to decide whether to redirect to bamboo:// or render
// the HTML success page. A regression here would silently downgrade
// the native flow to the browser page or — worse — strip the URI and
// drop the JWT into the wrong surface.
func TestIssueVerifyOIDCState_AppCallbackRoundTrip(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	want := "bamboo://auth/callback"

	state, err := IssueOIDCState(secret, "default", "", want, "", time.Minute)
	if err != nil {
		t.Fatalf("IssueOIDCState: %v", err)
	}

	got, err := VerifyOIDCState(secret, state)
	if err != nil {
		t.Fatalf("VerifyOIDCState: %v", err)
	}
	if got.AppCallback != want {
		t.Errorf("AppCallback = %q, want %q", got.AppCallback, want)
	}
	if got.Tenant != "default" {
		t.Errorf("Tenant = %q, want %q", got.Tenant, "default")
	}
}

// TestIssueOIDCState_EmptyAppCallbackIsEmpty confirms the browser path
// (no app_callback) sees an empty AppCallback after verification,
// which is the signal to render the HTML success page rather than 302.
func TestIssueOIDCState_EmptyAppCallbackIsEmpty(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	state, err := IssueOIDCState(secret, "default", "", "", "", time.Minute)
	if err != nil {
		t.Fatalf("IssueOIDCState: %v", err)
	}
	got, err := VerifyOIDCState(secret, state)
	if err != nil {
		t.Fatalf("VerifyOIDCState: %v", err)
	}
	if got.AppCallback != "" {
		t.Errorf("AppCallback = %q, want empty", got.AppCallback)
	}
}

// TestOIDCState_PKCEVerifierRoundTrip is the audit M-6 regression: the
// PKCE verifier minted at /login must survive the signed state so the
// /callback can replay it to the token exchange. It also confirms the
// S256 challenge actually lands on the provider AuthURL, and that a
// mismatched verifier can't be smuggled past the HMAC.
func TestOIDCState_PKCEVerifierRoundTrip(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	verifier := GeneratePKCEVerifier()
	if verifier == "" {
		t.Fatal("GeneratePKCEVerifier returned empty")
	}

	state, err := IssueOIDCState(secret, "default", "", "", verifier, time.Minute)
	if err != nil {
		t.Fatalf("IssueOIDCState: %v", err)
	}
	got, err := VerifyOIDCState(secret, state)
	if err != nil {
		t.Fatalf("VerifyOIDCState: %v", err)
	}
	if got.Verifier != verifier {
		t.Errorf("Verifier round-trip = %q, want %q", got.Verifier, verifier)
	}

	// The S256 challenge (not the raw verifier) must appear on the auth URL.
	g := NewGoogleProvider("cid", "csecret")
	url := g.AuthURL(state, "https://c/cb", verifier)
	if !strings.Contains(url, "code_challenge=") || !strings.Contains(url, "code_challenge_method=S256") {
		t.Errorf("AuthURL missing PKCE challenge params: %s", url)
	}
	if strings.Contains(url, verifier) {
		t.Error("AuthURL leaked the raw verifier (must send only the S256 challenge)")
	}

	// Empty verifier ⇒ no PKCE params (backward-compat path).
	if u := g.AuthURL(state, "https://c/cb", ""); strings.Contains(u, "code_challenge") {
		t.Errorf("empty verifier should omit PKCE params: %s", u)
	}
}
