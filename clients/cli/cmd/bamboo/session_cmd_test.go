// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/cli/internal/state"
	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/session"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func TestCLISessionCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clientKey, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfgDir, err := state.Dir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cfgDir, "session")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = session.Serve(ctx, ln, session.Config{
			PrivateKey: serverKey,
			Allow:      []wg.PublicKey{clientKey.PublicKey()},
			Root:       root,
			EnableExec: true,
		})
	}()

	prev := flagPeerKey
	t.Cleanup(func() { flagPeerKey = prev })
	flagPeerKey = serverKey.PublicKey().Base64()
	addr := ln.Addr().String()

	var stdout, stderr bytes.Buffer
	execCmd.SetOut(&stdout)
	execCmd.SetErr(&stderr)
	execCmd.SetContext(context.Background())
	if err := execCmd.RunE(execCmd, []string{addr, "/bin/echo", "cli-ok"}); err != nil {
		t.Fatalf("exec: %v stderr=%s", err, stderr.String())
	}
	if stdout.String() != "cli-ok\n" {
		t.Fatalf("exec stdout = %q", stdout.String())
	}

	local := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(local, []byte("file-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	filePutCmd.SetContext(context.Background())
	if err := filePutCmd.RunE(filePutCmd, []string{addr, local, "box/in.txt"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	gotLocal := filepath.Join(t.TempDir(), "out.txt")
	fileGetCmd.SetContext(context.Background())
	if err := fileGetCmd.RunE(fileGetCmd, []string{addr, "box/in.txt", gotLocal}); err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := os.ReadFile(gotLocal)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "file-body" {
		t.Fatalf("get body = %q", got)
	}

	msgSendCmd.SetContext(context.Background())
	if err := msgSendCmd.RunE(msgSendCmd, []string{addr, "hello peer"}); err != nil {
		t.Fatalf("msg: %v", err)
	}
	stdout.Reset()
	msgInboxCmd.SetOut(&stdout)
	msgInboxCmd.SetContext(context.Background())
	if err := msgInboxCmd.RunE(msgInboxCmd, nil); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(stdout.String(), "hello peer") || !strings.Contains(stdout.String(), clientKey.PublicKey().Base64()) {
		t.Fatalf("inbox = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	termCtx, termCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer termCancel()
	termCmd.SetIn(strings.NewReader("echo term-ok\nexit\n"))
	termCmd.SetOut(&stdout)
	termCmd.SetErr(&stderr)
	termCmd.SetContext(termCtx)
	if err := termCmd.RunE(termCmd, []string{addr}); err != nil {
		t.Fatalf("term: %v stdout=%q stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "term-ok") {
		t.Fatalf("term stdout = %q stderr=%s", stdout.String(), stderr.String())
	}

	// The listener is still in Accept. Cancel before the test's temp dirs go away.
	cancel()
	time.Sleep(20 * time.Millisecond)
	_ = ln.Close()
}

func TestPeerGateFollowsCache(t *testing.T) {
	if g := (peerGate{}); g.Allows(wg.PublicKey{}) {
		t.Fatal("nil cache allowed a key")
	}
	self := &bamboov1.Peer{Id: "self", Ip: "100.64.0.1"}
	otherPriv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	other := &bamboov1.Peer{
		Id:                 "a",
		Ip:                 "100.64.0.2",
		WireguardPublicKey: otherPriv.PublicKey().Base64(),
	}
	cache := clientsync.New(self, []*bamboov1.Peer{other})
	g := peerGate{cache: cache}
	if !g.Allows(otherPriv.PublicKey()) {
		t.Fatal("initial peer denied")
	}
	stranger, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	if g.Allows(stranger.PublicKey()) {
		t.Fatal("stranger allowed")
	}
	removed := cache.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerRemoved{
			PeerRemoved: &bamboov1.PeerRemoved{PeerId: "a"},
		},
	})
	if !removed {
		t.Fatal("remove did not change the cache")
	}
	if g.Allows(otherPriv.PublicKey()) {
		t.Fatal("removed peer still allowed")
	}
	added := &bamboov1.Peer{
		Id:                 "b",
		Ip:                 "100.64.0.3",
		WireguardPublicKey: stranger.PublicKey().Base64(),
	}
	if !cache.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerAdded{
			PeerAdded: &bamboov1.PeerAdded{Peer: added},
		},
	}) {
		t.Fatal("add did not change the cache")
	}
	if !g.Allows(stranger.PublicKey()) {
		t.Fatal("joined peer denied")
	}
}

func TestSessionListenAddr(t *testing.T) {
	got, err := sessionListenAddr("100.64.0.2")
	if err != nil || got != "100.64.0.2:54110" {
		t.Fatalf("v4 = %q %v", got, err)
	}
	got, err = sessionListenAddr("fdba:1100::1")
	if err != nil || got != "[fdba:1100::1]:54110" {
		t.Fatalf("v6 = %q %v", got, err)
	}
	if _, err := sessionListenAddr(""); err == nil {
		t.Fatal("empty ip accepted")
	}
	if _, err := sessionListenAddr("not-an-ip"); err == nil {
		t.Fatal("bad ip accepted")
	}
}
