// SPDX-License-Identifier: Apache-2.0

package relay

import "testing"

func TestNormalizeRelayURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare host gets /relay", "wss://relay.example.com", "wss://relay.example.com/relay"},
		{"trailing slash gets /relay", "wss://relay.example.com/", "wss://relay.example.com/relay"},
		{"explicit /relay untouched", "wss://relay.example.com/relay", "wss://relay.example.com/relay"},
		{"custom path untouched", "wss://relay.example.com/edge/ws", "wss://relay.example.com/edge/ws"},
		{"host:port bare gets /relay", "wss://relay.example.com:8443", "wss://relay.example.com:8443/relay"},
		{"ws scheme bare gets /relay", "ws://127.0.0.1:9000", "ws://127.0.0.1:9000/relay"},
		{"unparseable left unchanged", "://nonsense", "://nonsense"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeRelayURL(c.in); got != c.want {
				t.Errorf("normalizeRelayURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
