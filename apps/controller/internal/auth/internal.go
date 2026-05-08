// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

// signHMAC returns HMAC-SHA256(secret, body).
func signHMAC(secret, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return mac.Sum(nil)
}

// constantTimeEqual is a thin wrapper around hmac.Equal to keep oidc.go
// from importing the package directly (cleaner public API).
func constantTimeEqual(a, b []byte) bool {
	return hmac.Equal(a, b)
}

// splitOnDot is strings.Cut on '.' with a slightly nicer name in this file.
func splitOnDot(s string) (before, after string, ok bool) {
	return strings.Cut(s, ".")
}
