// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package session_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/session"
)

func TestPTYShell(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	p := startPair(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, err := session.DialTerm(ctx, p.addr, p.client, p.server.PublicKey(), nil, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.Close() }()
	if err := term.Resize(30, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(term, "echo hello-pty\nexit\n"); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		defer close(readDone)
		_, _ = io.Copy(&buf, term)
	}()
	type result struct {
		code int
		err  error
	}
	exitDone := make(chan result, 1)
	go func() {
		code, err := term.ExitCode()
		exitDone <- result{code, err}
	}()
	var got result
	select {
	case got = <-exitDone:
	case <-time.After(8 * time.Second):
		t.Fatalf("pty did not exit; output=%q", buf.String())
	}
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("pty output did not finish; output=%q", buf.String())
	}
	if got.err != nil {
		t.Fatalf("exit: %v output=%q", got.err, buf.String())
	}
	if got.code != 0 || !strings.Contains(buf.String(), "hello-pty") {
		t.Fatalf("code=%d output=%q", got.code, buf.String())
	}
}
