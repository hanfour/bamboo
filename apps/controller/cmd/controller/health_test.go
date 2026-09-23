// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestHealthURL(t *testing.T) {
	t.Setenv("BAMBOO_HTTP_ADDR", "")
	if got := healthURL(); got != "http://127.0.0.1:8081/healthz" {
		t.Fatalf("default = %s", got)
	}
	t.Setenv("BAMBOO_HTTP_ADDR", "0.0.0.0:8081")
	if got := healthURL(); got != "http://127.0.0.1:8081/healthz" {
		t.Fatalf("wildcard = %s", got)
	}
	t.Setenv("BAMBOO_HTTP_ADDR", "127.0.0.1:9090")
	if got := healthURL(); got != "http://127.0.0.1:9090/healthz" {
		t.Fatalf("explicit = %s", got)
	}
}
