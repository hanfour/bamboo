// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_validYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  grpc_addr: 0.0.0.0:8080
  http_addr: 0.0.0.0:8081
database:
  url: postgres://user:pass@localhost/db
redis:
  url: redis://localhost:6379
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  grpc_addr: 0.0.0.0:8080
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Error("expected error when database.url is missing, got nil")
	}
}

func TestLoad_envOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  grpc_addr: 0.0.0.0:8080
database:
  url: postgres://from-file
`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DATABASE_URL", "postgres://from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.URL != "postgres://from-env" {
		t.Errorf("Database.URL = %q, want postgres://from-env", cfg.Database.URL)
	}
}
