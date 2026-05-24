// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sort"
	"strings"
)

// RedactEnv renders the supplied environ slice as `name=value`
// lines, with values redacted whenever the name matches one of
// the secret-bearing patterns below. The function is exported for
// testability + to make the redaction grammar a single source of
// truth across the support-bundle implementation.
//
// Filtering rules:
//
//  1. Only BAMBOO_* variables are included. Everything else (PATH,
//     HOME, USER, …) carries OS-level surface area irrelevant to
//     bamboo support and is dropped silently. This is more
//     conservative than "include + redact" because Mac/Linux env
//     leaks things like SSH_AUTH_SOCK that the support team
//     shouldn't see.
//
//  2. A BAMBOO_* var whose name (case-insensitive) contains any of
//     SECRET / KEY / TOKEN / PASSWORD / COOKIE has its value
//     replaced with "<redacted N chars>" where N is the original
//     length — the length itself is useful diagnostic info ("is
//     this empty? is this the right format?") without leaking
//     the value.
//
//  3. Order: deterministic (lexicographic on name) so two runs on
//     the same env produce identical output. The support engineer
//     can diff bundles cleanly.
//
// What we DON'T do here:
//
//   - We don't try to redact values that happen to LOOK like
//     secrets (e.g. a long base64 string in a non-secret-named
//     env var). False-positive redaction would hide useful
//     debugging info; the operator who is responsible for naming
//     their env vars carries the trust here.
func RedactEnv(env []string) string {
	const placeholder = "<redacted>"
	type kv struct{ name, value string }
	var rows []kv
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		name, value := e[:idx], e[idx+1:]
		if !strings.HasPrefix(name, "BAMBOO_") {
			continue
		}
		if isSecretLikeName(name) {
			value = redactedToken(len(value))
			_ = placeholder // documented above; kept as a reference for the format
		}
		rows = append(rows, kv{name: name, value: value})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.name)
		b.WriteByte('=')
		b.WriteString(r.value)
		b.WriteByte('\n')
	}
	return b.String()
}

// isSecretLikeName matches the case-insensitive substrings the
// redaction rule considers sensitive. Pulled out so tests can
// exercise the grammar directly without going through RedactEnv.
func isSecretLikeName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"SECRET", "KEY", "TOKEN", "PASSWORD", "COOKIE"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// redactedToken renders the placeholder with the original value's
// length so the support engineer can spot common shape problems
// ("0 chars" = unset; "5 chars" = probably wrong) without seeing
// the value itself.
func redactedToken(n int) string {
	if n == 0 {
		return "<redacted, empty>"
	}
	var b strings.Builder
	b.WriteString("<redacted, ")
	// Inline strconv-equivalent to keep the dep surface zero — the
	// support-bundle file already pulls archive/zip + http; one
	// fewer import here keeps the secret-handling code visually
	// small.
	b.WriteString(intToString(n))
	b.WriteString(" chars>")
	return b.String()
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
