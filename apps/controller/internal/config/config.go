// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads, validates, and overrides controller configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level controller configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Database   DatabaseConfig   `yaml:"database"`
	Redis      RedisConfig      `yaml:"redis"`
	ClickHouse ClickHouseConfig `yaml:"clickhouse"`
	Auth       AuthConfig       `yaml:"auth"`
	WGSync     WGSyncConfig     `yaml:"wgsync"`
}

// WGSyncConfig configures the wg-state reporter that mirrors host
// WireGuard liveness into the peers table. An empty StatePath
// disables the reporter — appropriate for dev / non-VPS deploys.
//
// On a single-VPS production deploy, infra/full/bootstrap.sh
// installs a systemd timer that writes `wg show $IFACE dump` to
// the StatePath every Interval; the controller only needs read
// access (mounted ro into the container).
type WGSyncConfig struct {
	StatePath    string `yaml:"state_path"`
	Interval     string `yaml:"interval"`      // default 30s
	OnlineWindow string `yaml:"online_window"` // default 3m
}

// ServerConfig holds gRPC and HTTP listener addresses.
type ServerConfig struct {
	GRPCAddr string `yaml:"grpc_addr"`
	HTTPAddr string `yaml:"http_addr"`
}

// DatabaseConfig holds the Postgres DSN.
type DatabaseConfig struct {
	URL string `yaml:"url"`
}

// RedisConfig holds the Redis URL used for sessions and rate limiting.
type RedisConfig struct {
	URL string `yaml:"url"`
}

// ClickHouseConfig holds the optional ClickHouse DSN used for analytics.
// Empty URL puts the controller in degraded mode where telemetry writes
// drop after a single warning.
type ClickHouseConfig struct {
	URL string `yaml:"url"`
}

// AuthConfig holds session signing material and OIDC provider credentials.
type AuthConfig struct {
	// SessionSecret is the HMAC key used to sign session JWTs and OIDC
	// state tokens. Must be at least 32 bytes; rotate by re-issuing all
	// tokens.
	SessionSecret string `yaml:"session_secret"`
	// SessionTTL controls how long an issued session remains valid.
	// Defaults to 24h if empty.
	SessionTTL string        `yaml:"session_ttl"`
	OIDC       OIDCProviders `yaml:"oidc"`
}

// OIDCProviders bundles per-provider credentials.
type OIDCProviders struct {
	// BaseURL is the public-facing URL of the controller HTTP listener.
	// Used to build redirect URIs (e.g. https://controller.example.com).
	BaseURL string            `yaml:"base_url"`
	Google  ClientCredentials `yaml:"google"`
	GitHub  ClientCredentials `yaml:"github"`
}

// ClientCredentials is a generic OAuth client_id / client_secret pair.
type ClientCredentials struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// Load reads the YAML at path (when non-empty), applies environment
// overrides, and validates. When path is empty OR the file does not
// exist, Load skips the file and relies entirely on environment
// variables — useful for container deploys where every value comes
// from the orchestrator's secret store.
func Load(path string) (*Config, error) {
	cfg := defaultConfig()
	if path != "" {
		// path is operator-supplied via --config flag; intentional file inclusion.
		raw, err := os.ReadFile(path) //nolint:gosec // G304: trusted operator input
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read config %q: %w", path, err)
			}
			// File doesn't exist; fall through to env-only mode. The
			// validate() call below catches any missing required values.
		} else {
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return nil, fmt.Errorf("parse config %q: %w", path, err)
			}
		}
	}

	cfg.applyEnvOverrides()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// defaultConfig returns a Config pre-populated with safe defaults so
// the env-only path doesn't have to set values like grpc_addr just
// to satisfy validation. These mirror config/example.yaml.
func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			GRPCAddr: "0.0.0.0:8080",
			HTTPAddr: "0.0.0.0:8081",
		},
		Auth: AuthConfig{
			SessionTTL: "24h",
		},
	}
}

// applyEnvOverrides lets operators override secrets without committing them.
// Environment variables win over file values.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("BAMBOO_GRPC_ADDR"); v != "" {
		c.Server.GRPCAddr = v
	}
	if v := os.Getenv("BAMBOO_HTTP_ADDR"); v != "" {
		c.Server.HTTPAddr = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		c.Database.URL = v
	}
	if v := os.Getenv("REDIS_URL"); v != "" {
		c.Redis.URL = v
	}
	if v := os.Getenv("CLICKHOUSE_URL"); v != "" {
		c.ClickHouse.URL = v
	}
	if v := os.Getenv("OIDC_GOOGLE_CLIENT_ID"); v != "" {
		c.Auth.OIDC.Google.ClientID = v
	}
	if v := os.Getenv("OIDC_GOOGLE_CLIENT_SECRET"); v != "" {
		c.Auth.OIDC.Google.ClientSecret = v
	}
	if v := os.Getenv("OIDC_GITHUB_CLIENT_ID"); v != "" {
		c.Auth.OIDC.GitHub.ClientID = v
	}
	if v := os.Getenv("OIDC_GITHUB_CLIENT_SECRET"); v != "" {
		c.Auth.OIDC.GitHub.ClientSecret = v
	}
	if v := os.Getenv("BAMBOO_SESSION_SECRET"); v != "" {
		c.Auth.SessionSecret = v
	}
	if v := os.Getenv("BAMBOO_BASE_URL"); v != "" {
		c.Auth.OIDC.BaseURL = v
	}
	if v := os.Getenv("BAMBOO_WG_STATE_PATH"); v != "" {
		c.WGSync.StatePath = v
	}
	if v := os.Getenv("BAMBOO_WG_SYNC_INTERVAL"); v != "" {
		c.WGSync.Interval = v
	}
	if v := os.Getenv("BAMBOO_WG_ONLINE_WINDOW"); v != "" {
		c.WGSync.OnlineWindow = v
	}
}

// validate enforces minimum required fields.
func (c *Config) validate() error {
	if c.Server.GRPCAddr == "" {
		return fmt.Errorf("server.grpc_addr is required")
	}
	if c.Database.URL == "" {
		return fmt.Errorf("database.url is required (or set DATABASE_URL)")
	}
	if c.Auth.SessionSecret == "" {
		return fmt.Errorf("auth.session_secret is required (or set BAMBOO_SESSION_SECRET)")
	}
	if len(c.Auth.SessionSecret) < 32 {
		return fmt.Errorf("auth.session_secret must be at least 32 bytes")
	}
	return nil
}
