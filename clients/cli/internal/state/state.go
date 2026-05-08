// SPDX-License-Identifier: Apache-2.0

// Package state persists the bamboo agent's local identity across
// invocations.
//
// Files live under $XDG_CONFIG_HOME/bamboo (defaulting to
// ~/.config/bamboo). The private key is mode 0600 so other local
// users cannot read it.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanfour/bamboo/clients/core/wg"
)

// Dir returns the configuration directory used by the agent. It is
// created if missing.
func Dir() (string, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		xdg = filepath.Join(home, ".config")
	}
	dir := filepath.Join(xdg, "bamboo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return dir, nil
}

// LoadOrCreatePrivateKey reads the persisted private key from disk, or
// generates a new one and writes it (mode 0600) on first use.
func LoadOrCreatePrivateKey() (wg.PrivateKey, error) {
	dir, err := Dir()
	if err != nil {
		return wg.PrivateKey{}, err
	}
	path := filepath.Join(dir, "private_key")

	raw, err := os.ReadFile(path) //nolint:gosec // path is under our XDG dir
	if err == nil {
		k, err := wg.PrivateKeyFromBase64(string(trimSpace(raw)))
		if err != nil {
			return wg.PrivateKey{}, fmt.Errorf("decode existing key: %w", err)
		}
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return wg.PrivateKey{}, fmt.Errorf("read key: %w", err)
	}

	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		return wg.PrivateKey{}, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(path, []byte(priv.Base64()+"\n"), 0o600); err != nil {
		return wg.PrivateKey{}, fmt.Errorf("write key: %w", err)
	}
	return priv, nil
}

// trimSpace strips leading/trailing whitespace bytes.
func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
