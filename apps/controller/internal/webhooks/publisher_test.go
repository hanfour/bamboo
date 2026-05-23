// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooks_test

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/webhooks"
)

// TestSignature_DeterministicAndKeyed pins the HMAC contract:
//
//   - same (secret, body) ⇒ same sig (deterministic).
//   - different body ⇒ different sig.
//   - different secret ⇒ different sig.
//
// The constant-time-compare requirement is the receiver's
// responsibility; we just verify the bytes round-trip.
func TestSignature_DeterministicAndKeyed(t *testing.T) {
	body := []byte(`{"id":"e1","type":"peer.register"}`)
	secret := "shared-secret"

	first := webhooks.Signature(secret, body)
	second := webhooks.Signature(secret, body)
	if first != second {
		t.Errorf("non-deterministic signature: %q vs %q", first, second)
	}
	if other := webhooks.Signature(secret, []byte(`{"id":"e2"}`)); other == first {
		t.Errorf("different body yielded same sig: %q", other)
	}
	if other := webhooks.Signature("different", body); other == first {
		t.Errorf("different secret yielded same sig: %q", other)
	}
}

// The full Hook → channel → worker → HTTP POST loop is covered by
// the e2e seam test in apps/controller/test/e2e/rest_webhooks_test.go,
// which spins up an httptest receiver, posts a webhook subscription
// pointing at it, triggers an audit event via a peer registration,
// and asserts the receiver got a signed POST with the expected
// payload. The publisher package itself stays a thin orchestrator —
// no in-package unit test would meaningfully cover the wiring that
// the e2e doesn't already prove.
