// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"
	"time"
)

// TestAuditRetentionWindow pins the env-var → duration table. A
// regression here would either silently disable the reaper (env
// parsed as 0) or silently keep audit forever (default fallback
// when the operator meant to enable). Both are SOC-2-relevant
// failure modes.
func TestAuditRetentionWindow(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		setEnv    bool
		wantHours float64
	}{
		{
			name:      "unset uses default 365d",
			setEnv:    false,
			wantHours: 365 * 24,
		},
		{
			name:      "explicit 30",
			env:       "30",
			setEnv:    true,
			wantHours: 30 * 24,
		},
		{
			name:      "0 disables (operator opt-out)",
			env:       "0",
			setEnv:    true,
			wantHours: 0,
		},
		{
			name:      "negative also disables",
			env:       "-1",
			setEnv:    true,
			wantHours: 0,
		},
		{
			name: "whitespace tolerated",
			// Operator config tools (k8s manifest, docker-compose) often
			// leave trailing whitespace; tolerating it avoids a
			// "why isn't my retention working" debug session.
			env:       "  90  ",
			setEnv:    true,
			wantHours: 90 * 24,
		},
		{
			name:      "unparseable disables (loud)",
			env:       "thirty",
			setEnv:    true,
			wantHours: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("BAMBOO_AUDIT_RETENTION_DAYS", tc.env)
			} else {
				// t.Setenv to "" still sets-as-empty; unset semantics
				// need explicit Unsetenv-style. Setenv to "" works
				// because auditRetentionWindow treats "" as default.
				t.Setenv("BAMBOO_AUDIT_RETENTION_DAYS", "")
			}
			got := auditRetentionWindow()
			if got.Hours() != tc.wantHours {
				t.Errorf("env=%q ⇒ %.0fh, want %.0fh", tc.env, got.Hours(), tc.wantHours)
			}
		})
	}
}

// TestAuditRetentionWindow_DefaultIsAYear pins the magic 365 to
// the const so a future refactor (say, dropping to 90d "for cost"
// without a roadmap update) is caught by the test, not by an
// auditor's question post-deploy.
func TestAuditRetentionWindow_DefaultIsAYear(t *testing.T) {
	t.Setenv("BAMBOO_AUDIT_RETENTION_DAYS", "")
	got := auditRetentionWindow()
	want := time.Duration(auditRetentionDaysDefault) * 24 * time.Hour
	if got != want {
		t.Errorf("default = %v, want %v", got, want)
	}
}
