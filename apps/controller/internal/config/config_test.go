// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validBody = `
server:
  grpc_addr: 0.0.0.0:8080
  http_addr: 0.0.0.0:8081
database:
  url: postgres://user:pass@localhost/db
redis:
  url: redis://localhost:6379
auth:
  session_secret: "test-secret-at-least-32-bytes-long-padding"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_validYAML(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GRPCAddr != "0.0.0.0:8080" {
		t.Errorf("GRPCAddr = %q, want 0.0.0.0:8080", cfg.Server.GRPCAddr)
	}
	if cfg.Database.URL == "" {
		t.Error("Database.URL is empty")
	}
}

func TestLoad_missingDatabase(t *testing.T) {
	body := strings.ReplaceAll(validBody, "database:\n  url: postgres://user:pass@localhost/db\n", "")
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Error("expected error when database.url is missing, got nil")
	}
}

func TestLoad_missingSessionSecret(t *testing.T) {
	body := strings.ReplaceAll(validBody, `  session_secret: "test-secret-at-least-32-bytes-long-padding"`, "")
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Error("expected error when session_secret is missing, got nil")
	}
}

func TestLoad_shortSessionSecret(t *testing.T) {
	body := strings.ReplaceAll(validBody, `"test-secret-at-least-32-bytes-long-padding"`, `"too-short"`)
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Error("expected error for short session_secret, got nil")
	}
}

func TestLoad_envOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://from-env")
	cfg, err := Load(writeConfig(t, validBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.URL != "postgres://from-env" {
		t.Errorf("Database.URL = %q, want postgres://from-env", cfg.Database.URL)
	}
}
