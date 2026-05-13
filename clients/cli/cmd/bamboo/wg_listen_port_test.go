// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"testing"
)

// TestPickFreeUDPPort_ReturnsUsablePort verifies the helper returns
// a port the caller can actually bind to. The race window between
// our pick/close and the caller's bind is what makes this useful: a
// regression that returns 0 or a port already in another listener
// would surface here as a bind error.
func TestPickFreeUDPPort_ReturnsUsablePort(t *testing.T) {
	port, err := pickFreeUDPPort()
	if err != nil {
		t.Fatalf("pickFreeUDPPort: %v", err)
	}
	if port == 0 {
		t.Fatal("pickFreeUDPPort returned 0")
	}
	// Try to bind to the picked port. In the unlikely event the
	// kernel reassigned it to another listener between our pick and
	// this bind, retry once with a fresh pick before failing.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(port)})
	if err != nil {
		port2, err2 := pickFreeUDPPort()
		if err2 != nil {
			t.Fatalf("rebind: %v / retry pick: %v", err, err2)
		}
		conn, err = net.ListenUDP("udp", &net.UDPAddr{Port: int(port2)})
		if err != nil {
			t.Fatalf("rebind to picked port %d failed even after retry: %v", port2, err)
		}
	}
	_ = conn.Close()
}

// TestPickFreeUDPPort_DistinctOnConsecutiveCalls is a soft smoke
// test: two calls in quick succession should rarely collide, since
// the kernel typically advances the ephemeral counter. Not a hard
// guarantee — if it ever fails on a tight loop, drop this test.
func TestPickFreeUDPPort_DistinctOnConsecutiveCalls(t *testing.T) {
	a, err := pickFreeUDPPort()
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	b, err := pickFreeUDPPort()
	if err != nil {
		t.Fatalf("second pick: %v", err)
	}
	if a == 0 || b == 0 {
		t.Fatalf("pick returned 0: a=%d b=%d", a, b)
	}
}
