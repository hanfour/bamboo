// SPDX-License-Identifier: Apache-2.0

package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanfour/bamboo/clients/cli/internal/state"
)

func TestLoadOrCreatePrivateKey_PersistsAcrossCalls(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	first, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Error("private key was not stable across calls")
	}
}

func TestLoadOrCreatePrivateKey_FileMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if _, err := state.LoadOrCreatePrivateKey(); err != nil {
		t.Fatalf("first: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "bamboo", "private_key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}
