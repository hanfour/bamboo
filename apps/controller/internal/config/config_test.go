// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestApplyEnvOverrides_ReleaseFeed pins the three BAMBOO_RELEASE_FEED_*
// env vars surface into the Config struct as expected, including the
// 5-minute floor on interval (rate-limit headroom against GitHub's
// 60-req/hr unauthenticated quota).
func TestApplyEnvOverrides_ReleaseFeed(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "")
		var c Config
		c.applyEnvOverrides()
		if !c.ReleaseFeed.IsEnabled() {
			t.Errorf("Enabled default = false, want true")
		}
		if c.ReleaseFeed.Repo != "hanfour/bamboo" {
			t.Errorf("Repo default = %q, want hanfour/bamboo", c.ReleaseFeed.Repo)
		}
		if c.ReleaseFeed.Interval != time.Hour {
			t.Errorf("Interval default = %v, want 1h", c.ReleaseFeed.Interval)
		}
	})
	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "false")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "myorg/mybamboo")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "30m")
		var c Config
		c.applyEnvOverrides()
		if c.ReleaseFeed.IsEnabled() {
			t.Errorf("Enabled = true, want false")
		}
		if c.ReleaseFeed.Repo != "myorg/mybamboo" {
			t.Errorf("Repo = %q, want myorg/mybamboo", c.ReleaseFeed.Repo)
		}
		if c.ReleaseFeed.Interval != 30*time.Minute {
			t.Errorf("Interval = %v, want 30m", c.ReleaseFeed.Interval)
		}
	})
	t.Run("interval clamps to 5m floor", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "1m")
		var c Config
		c.applyEnvOverrides()
		c.validate() //nolint:errcheck // testing clamp side-effect only
		if c.ReleaseFeed.Interval != 5*time.Minute {
			t.Errorf("Interval = %v after clamp, want 5m", c.ReleaseFeed.Interval)
		}
	})
	t.Run("invalid repo disables feed", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "true")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "no-slash-no-repo")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "")
		var c Config
		c.applyEnvOverrides()
		c.validate() //nolint:errcheck // testing disable side-effect only
		if c.ReleaseFeed.IsEnabled() {
			t.Errorf("Enabled = true after invalid repo, want false")
		}
	})
	t.Run("yaml-disabled honoured when env unset", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "")
		f := false
		c := Config{ReleaseFeed: ReleaseFeedConfig{Enabled: &f}}
		c.applyEnvOverrides()
		if c.ReleaseFeed.IsEnabled() {
			t.Errorf("YAML enabled=false was clobbered, want preserved")
		}
	})
}
