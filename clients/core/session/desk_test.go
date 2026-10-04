// SPDX-License-Identifier: AGPL-3.0-or-later

package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/clients/core/session"
)

func TestDeskMessageAndFiles(t *testing.T) {
	root := t.TempDir()
	desk, err := session.OpenDesk(root, false)
	if err != nil {
		t.Fatal(err)
	}
	reply, term := desk.Handle(session.DeskOp{Kind: session.DeskMsg, From: "admin@example.com", Text: "hello desk"})
	if term != nil || !reply.OK {
		t.Fatalf("reply %+v term %v", reply, term != nil)
	}
	inbox, err := session.ReadInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Text != "hello desk" || inbox[0].From != "admin@example.com" {
		t.Fatalf("inbox %+v", inbox)
	}

	reply, _ = desk.Handle(session.DeskOp{Kind: session.DeskPut, Path: "notes/a.txt", Data: []byte("body")})
	if !reply.OK {
		t.Fatalf("put %+v", reply)
	}
	reply, _ = desk.Handle(session.DeskOp{Kind: session.DeskGet, Path: "notes/a.txt"})
	if !reply.OK || string(reply.Data) != "body" {
		t.Fatalf("get %+v", reply)
	}
	reply, _ = desk.Handle(session.DeskOp{Kind: session.DeskPut, Path: "../secret", Data: []byte("no")})
	if reply.OK || !strings.Contains(reply.Error, session.ErrPath) {
		t.Fatalf("escape %+v", reply)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "secret")); err == nil {
		t.Fatal("wrote outside the root")
	}
}

func TestDeskTerminalRequiresExec(t *testing.T) {
	desk, err := session.OpenDesk(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	reply, term := desk.Handle(session.DeskOp{Kind: session.DeskPTYStart})
	if term != nil || !strings.Contains(reply.Error, session.ErrExecOff) {
		t.Fatalf("reply %+v", reply)
	}
}
