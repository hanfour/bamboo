// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/session"
)

func TestDeskShell(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	desk, err := session.OpenDesk(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	reply, term := desk.Handle(session.DeskOp{Kind: session.DeskPTYStart, Rows: 24, Cols: 80})
	if !reply.OK || term == nil {
		t.Fatalf("start %+v", reply)
	}
	wrote, _ := desk.Handle(session.DeskOp{Kind: session.DeskPTYData, Data: []byte("echo desk-ok\nexit\n")})
	if !wrote.OK {
		t.Fatalf("write %+v", wrote)
	}
	var out strings.Builder
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case b, ok := <-term.Out:
			if !ok {
				if !strings.Contains(out.String(), "desk-ok") {
					t.Fatalf("output %q", out.String())
				}
				if term.Code() != 0 {
					t.Fatalf("exit %d", term.Code())
				}
				return
			}
			out.Write(b)
		case <-timer.C:
			t.Fatalf("timeout, output %q", out.String())
		}
	}
}
