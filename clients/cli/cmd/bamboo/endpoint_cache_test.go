// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
)

func TestEndpointCacheKeepsLastWhenPortIsHeld(t *testing.T) {
	var cache endpointCache
	first := cache.observe([]string{"203.0.113.5:51820"}, nil)
	if len(first) != 1 || first[0] != "203.0.113.5:51820" {
		t.Fatalf("first = %v", first)
	}
	first[0] = "mutated"

	held := fmt.Errorf("dial stun: %w", syscall.EADDRINUSE)
	again := cache.observe(nil, held)
	if len(again) != 1 || again[0] != "203.0.113.5:51820" {
		t.Fatalf("held = %v", again)
	}
	again[0] = "mutated"
	third := cache.observe(nil, held)
	if len(third) != 1 || third[0] != "203.0.113.5:51820" {
		t.Fatalf("held again = %v", third)
	}
}

func TestEndpointCacheOtherErrorDoesNotReplaceLast(t *testing.T) {
	var cache endpointCache
	cache.observe([]string{"203.0.113.5:1"}, nil)
	if got := cache.observe(nil, errors.New("timeout")); got != nil {
		t.Fatalf("other error = %v, want nil", got)
	}
	got := cache.observe(nil, syscall.EADDRINUSE)
	if len(got) != 1 || got[0] != "203.0.113.5:1" {
		t.Fatalf("after other error, held = %v", got)
	}
}

func TestEndpointCacheEmptyWhenPortHeldBeforeAnyDiscovery(t *testing.T) {
	var cache endpointCache
	if got := cache.observe(nil, syscall.EADDRINUSE); got != nil {
		t.Fatalf("empty held = %v", got)
	}
}

func TestDiscoverEndpointsReusesEndpointWhenListenPortIsHeld(t *testing.T) {
	stunEndpoints = endpointCache{}
	t.Cleanup(func() { stunEndpoints = endpointCache{} })

	stunEndpoints.observe([]string{"203.0.113.9:9"}, nil)
	port, err := pickFreeUDPPort()
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: int(port)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = conn.Close() }()

	got := discoverEndpoints(port)
	if len(got) != 1 || got[0] != "203.0.113.9:9" {
		t.Fatalf("discover while held = %v", got)
	}
}
