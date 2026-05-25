// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"
	"time"
)

// TestResolveSessionTTL pins the env → config → fallback precedence.
// Session TTL is a SOC 2 "session validity" control; a regression here
// would either silently extend (env unset, ignored value) or silently
// shorten (operator typo) session windows, both of which auditors
// flag.
func TestResolveSessionTTL(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		setEnv   bool
		cfgRaw   string
		fallback time.Duration
		want     time.Duration
	}{
		{
			name:     "no env, no cfg ⇒ fallback",
			fallback: 24 * time.Hour,
			want:     24 * time.Hour,
		},
		{
			name:     "cfg only",
			cfgRaw:   "8h",
			fallback: 24 * time.Hour,
			want:     8 * time.Hour,
		},
		{
			name:     "env wins over cfg",
			env:      "4",
			setEnv:   true,
			cfgRaw:   "24h",
			fallback: 24 * time.Hour,
			want:     4 * time.Hour,
		},
		{
			name:     "env whitespace tolerated",
			env:      "  2  ",
			setEnv:   true,
			fallback: 24 * time.Hour,
			want:     2 * time.Hour,
		},
		{
			name:     "env=0 ignored (fall back to cfg)",
			env:      "0",
			setEnv:   true,
			cfgRaw:   "6h",
			fallback: 24 * time.Hour,
			want:     6 * time.Hour,
		},
		{
			name:     "env negative ignored",
			env:      "-1",
			setEnv:   true,
			cfgRaw:   "12h",
			fallback: 24 * time.Hour,
			want:     12 * time.Hour,
		},
		{
			name:     "env unparseable ignored",
			env:      "eight",
			setEnv:   true,
			cfgRaw:   "5h",
			fallback: 24 * time.Hour,
			want:     5 * time.Hour,
		},
		{
			name:     "env unparseable + no cfg ⇒ fallback",
			env:      "nope",
			setEnv:   true,
			fallback: 24 * time.Hour,
			want:     24 * time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("BAMBOO_SESSION_TTL_HOURS", tc.env)
			} else {
				t.Setenv("BAMBOO_SESSION_TTL_HOURS", "")
			}
			got := resolveSessionTTL(tc.cfgRaw, tc.fallback)
			if got != tc.want {
				t.Errorf("env=%q cfg=%q ⇒ %v, want %v", tc.env, tc.cfgRaw, got, tc.want)
			}
		})
	}
}
