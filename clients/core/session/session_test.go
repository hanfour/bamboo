// SPDX-License-Identifier: AGPL-3.0-or-later

package session_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/session"
	"github.com/hanfour/bamboo/clients/core/wg"
)

func mustKey(t *testing.T) wg.PrivateKey {
	t.Helper()
	k, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return k
}

type pair struct {
	addr   string
	server wg.PrivateKey
	client wg.PrivateKey
	root   string
	calls  *atomic.Int32
}

func startPair(t *testing.T, enableExec bool) pair {
	t.Helper()
	return startPairAt(t, enableExec, nil)
}

func startPairAt(t *testing.T, enableExec bool, now func() time.Time) pair {
	t.Helper()
	serverKey := mustKey(t)
	clientKey := mustKey(t)
	root := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	cfg := session.Config{
		PrivateKey: serverKey,
		Allow:      []wg.PublicKey{clientKey.PublicKey()},
		Root:       root,
		EnableExec: enableExec,
		Now:        now,
		Run: func(_ context.Context, argv []string) ([]byte, []byte, int, error) {
			calls.Add(1)
			return []byte("ran:" + strings.Join(argv, " ")), []byte("err"), 7, nil
		},
	}
	go func() { _ = session.Serve(ctx, ln, cfg) }()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	return pair{
		addr:   ln.Addr().String(),
		server: serverKey,
		client: clientKey,
		root:   root,
		calls:  &calls,
	}
}

func TestExecPutGetMsg_AllowedPeer(t *testing.T) {
	p := startPair(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"uname", "-n"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got.ExitCode != 7 || string(got.Stdout) != "ran:uname -n" || string(got.Stderr) != "err" {
		t.Fatalf("exec result = %+v", got)
	}

	payload := []byte{0x00, 0xff, 'h', 'i'}
	if _, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpPut,
		Path: "notes/a.txt",
		Data: payload,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err = session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpGet,
		Path: "notes/a.txt",
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.Data, payload) {
		t.Fatalf("get = %x", got.Data)
	}
	onDisk, err := os.ReadFile(filepath.Join(p.root, "notes", "a.txt"))
	if err != nil {
		t.Fatalf("disk: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("disk = %x", onDisk)
	}

	if _, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpMsg,
		Text: "ping from alice",
	}); err != nil {
		t.Fatalf("msg: %v", err)
	}
	inbox, err := session.ReadInbox(p.root)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].Text != "ping from alice" || inbox[0].From != p.client.PublicKey().Base64() {
		t.Fatalf("inbox = %+v", inbox)
	}
}

func TestStrangerIsDeniedAndDoesNotRun(t *testing.T) {
	p := startPair(t, true)
	stranger := mustKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.Call(ctx, p.addr, stranger, p.server.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"uname"},
	})
	if err == nil {
		t.Fatal("stranger call succeeded")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want 0", p.calls.Load())
	}
}

func TestExecDisabled(t *testing.T) {
	p := startPair(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"uname"},
	})
	if err == nil || !strings.Contains(err.Error(), session.ErrExecOff) {
		t.Fatalf("err = %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want 0", p.calls.Load())
	}
}

func TestPathEscapeRejected(t *testing.T) {
	p := startPair(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []string{"../secret", "/etc/passwd", "inbox.jsonl", "link/x"}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(p.root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range cases {
		_, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
			Op:   session.OpPut,
			Path: path,
			Data: []byte("x"),
		})
		if err == nil || !strings.Contains(err.Error(), session.ErrPath) {
			t.Fatalf("path %q err = %v", path, err)
		}
	}
	_, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "nope" {
		t.Fatalf("outside changed: %q %v", got, err)
	}
}

func TestClockSkewDenied(t *testing.T) {
	p := startPairAt(t, false, func() time.Time { return time.Now().Add(time.Hour) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpMsg,
		Text: "late",
	})
	if err == nil {
		t.Fatal("skewed server accepted the handshake")
	}
	inbox, err := session.ReadInbox(p.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("inbox = %+v", inbox)
	}
}

func TestRealExecEcho(t *testing.T) {
	serverKey := mustKey(t)
	clientKey := mustKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := session.Config{
		PrivateKey: serverKey,
		Allow:      []wg.PublicKey{clientKey.PublicKey()},
		Root:       t.TempDir(),
		EnableExec: true,
	}
	go func() { _ = session.Serve(ctx, ln, cfg) }()
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	got, err := session.Call(callCtx, ln.Addr().String(), clientKey, serverKey.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"/bin/echo", "hello-mesh"},
	})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if got.ExitCode != 0 || string(got.Stdout) != "hello-mesh\n" {
		t.Fatalf("echo result = %q exit %d", got.Stdout, got.ExitCode)
	}
}

func TestEmptyAllowDeniesEveryone(t *testing.T) {
	key := mustKey(t)
	serverKey := mustKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	go func() {
		_ = session.Serve(ctx, ln, session.Config{
			PrivateKey: serverKey,
			Root:       t.TempDir(),
			EnableExec: true,
			Run: func(context.Context, []string) ([]byte, []byte, int, error) {
				calls.Add(1)
				return nil, nil, 0, nil
			},
		})
	}()
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	_, err = session.Call(callCtx, ln.Addr().String(), key, serverKey.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"uname"},
	})
	if err == nil {
		t.Fatal("empty allowlist accepted a peer")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestGetMissing(t *testing.T) {
	p := startPair(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpGet,
		Path: "missing.txt",
	})
	if err == nil || !strings.Contains(err.Error(), session.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadInboxMissing(t *testing.T) {
	entries, err := session.ReadInbox(t.TempDir())
	if err != nil || entries != nil {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestAllowListUpdatesWhileServing(t *testing.T) {
	serverKey := mustKey(t)
	clientKey := mustKey(t)
	list := session.NewAllowList(nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = session.Serve(ctx, ln, session.Config{
			PrivateKey: serverKey,
			Check:      list,
			Root:       t.TempDir(),
		})
	}()
	call := func(text string) error {
		callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer callCancel()
		_, err := session.Call(callCtx, ln.Addr().String(), clientKey, serverKey.PublicKey(), session.Request{
			Op:   session.OpMsg,
			Text: text,
		})
		return err
	}
	if err := call("before"); err == nil {
		t.Fatal("empty allowlist accepted a peer")
	}
	list.Replace([]wg.PublicKey{clientKey.PublicKey()})
	if err := call("after"); err != nil {
		t.Fatalf("updated allowlist: %v", err)
	}
	list.Replace(nil)
	if err := call("removed"); err == nil {
		t.Fatal("removed key was still accepted")
	}
}

func TestServeRejectsMissingRoot(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	err = session.Serve(context.Background(), ln, session.Config{PrivateKey: mustKey(t)})
	if err == nil {
		t.Fatal("expected root error")
	}
}

func TestRealExecExitCode(t *testing.T) {
	serverKey := mustKey(t)
	clientKey := mustKey(t)
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
			Root:       t.TempDir(),
			EnableExec: true,
		})
	}()
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	got, err := session.Call(callCtx, ln.Addr().String(), clientKey, serverKey.PublicKey(), session.Request{
		Op:   session.OpExec,
		Argv: []string{"/bin/sh", "-c", "echo out; echo err >&2; exit 3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 3 || string(got.Stdout) != "out\n" || string(got.Stderr) != "err\n" {
		t.Fatalf("result stdout=%q stderr=%q exit=%d", got.Stdout, got.Stderr, got.ExitCode)
	}
}

func TestPutParentIsFile(t *testing.T) {
	p := startPair(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpPut,
		Path: "blocked",
		Data: []byte("a"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := session.Call(ctx, p.addr, p.client, p.server.PublicKey(), session.Request{
		Op:   session.OpPut,
		Path: "blocked/child",
		Data: []byte("b"),
	})
	if err == nil {
		t.Fatal("put through a file parent succeeded")
	}
	if !strings.Contains(err.Error(), session.ErrBadReq) && !strings.Contains(err.Error(), session.ErrPath) {
		t.Fatalf("err = %v", err)
	}
}

func TestPTYRejectedWhenExecOff(t *testing.T) {
	p := startPair(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.DialTerm(ctx, p.addr, p.client, p.server.PublicKey(), nil, 24, 80)
	if err == nil || !strings.Contains(err.Error(), session.ErrExecOff) {
		t.Fatalf("err = %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want 0", p.calls.Load())
	}
}

func TestPTYBadArgv(t *testing.T) {
	p := startPair(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.DialTerm(ctx, p.addr, p.client, p.server.PublicKey(), []string{""}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), session.ErrBadReq) {
		t.Fatalf("err = %v", err)
	}
}
