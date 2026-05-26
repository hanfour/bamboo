// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads, validates, and overrides controller configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// parseInt is a thin wrapper so applyEnvOverrides can swallow the
// import without forcing strconv on every test file. Returns an
// error on non-numeric input so the caller can fall through to the
// file value rather than silently zeroing the field.
func parseInt(s string) (int, error) { return strconv.Atoi(s) }

// Config is the top-level controller configuration.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Database    DatabaseConfig    `yaml:"database"`
	Redis       RedisConfig       `yaml:"redis"`
	ClickHouse  ClickHouseConfig  `yaml:"clickhouse"`
	Auth        AuthConfig        `yaml:"auth"`
	WGSync      WGSyncConfig      `yaml:"wgsync"`
	SMTP        SMTPConfig        `yaml:"smtp"`
	ReleaseFeed ReleaseFeedConfig `yaml:"release_feed"`
}

// ReleaseFeedConfig drives the GitHub-releases poller that powers
// the PeerTable version-upgrade indicator. Operators in air-gapped
// deployments should set Enabled=false; everyone else leaves the
// defaults and gets the indicator for free.
type ReleaseFeedConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Repo     string        `yaml:"repo"`
	Interval time.Duration `yaml:"interval"`
}

// SMTPConfig configures the optional outbound SMTP relay used for
// invitation emails. When Host is empty the controller skips email
// delivery and falls back to "admin shares the token out-of-band"
// (the API response still includes the plaintext token; just no email
// is sent). Phase A target is a single relay shared across tenants;
// per-tenant SMTP belongs in a future SaaS-mode follow-up.
type SMTPConfig struct {
	// Host (and Port, default 587) of the SMTP relay. Empty disables
	// email delivery entirely.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// User / Pass for PLAIN auth. Empty pair disables auth (useful for
	// a sidecar relay on the same host that trusts loopback).
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
	// From is the literal envelope-from + display From: header. Should
	// match the relay's policy (some relays bounce mismatched From).
	From string `yaml:"from"`
	// PublicBaseURL is the URL invitees should visit, e.g.
	// https://bamboo.miilink.net. Used to build the /invite?token=...
	// link in the email body. Falls back to Auth.OIDC.BaseURL when
	// empty (which is usually the same value on a single-domain
	// deploy).
	PublicBaseURL string `yaml:"public_base_url"`
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
	SessionTTL string `yaml:"session_ttl"`
	// RequireAuth, when true, switches the controller into prod-mode
	// auth: REST /api/v1/* endpoints require a valid session JWT
	// (Authorization bearer or bamboo_session cookie). The X-Tenant-Slug
	// header is no longer accepted as a credential and the implicit
	// "default" tenant fallback is disabled. /api/v1/me still responds
	// so the Web UI can render an unauthenticated landing state, and
	// the peer-id-only paths (/peers/register, /peers/heartbeat,
	// /peers/watch) keep their existing pre-auth-key + peer_id flows.
	// Defaults to false so `make local-up` and existing dev workflows
	// keep working unchanged.
	RequireAuth bool          `yaml:"require_auth"`
	OIDC        OIDCProviders `yaml:"oidc"`
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
	if v := os.Getenv("BAMBOO_REQUIRE_AUTH"); v != "" {
		// Any value other than "true" / "1" disables prod mode; this
		// avoids accidental enables from leftover empty exports.
		c.Auth.RequireAuth = v == "true" || v == "1"
	}
	if v := os.Getenv("BAMBOO_BASE_URL"); v != "" {
		c.Auth.OIDC.BaseURL = v
	}
	if v := os.Getenv("BAMBOO_SMTP_HOST"); v != "" {
		c.SMTP.Host = v
	}
	if v := os.Getenv("BAMBOO_SMTP_PORT"); v != "" {
		// Best-effort parse; invalid ints fall back to the file value /
		// the zero value (validate-on-use catches the latter).
		if n, err := parseInt(v); err == nil && n > 0 {
			c.SMTP.Port = n
		}
	}
	if v := os.Getenv("BAMBOO_SMTP_USER"); v != "" {
		c.SMTP.User = v
	}
	if v := os.Getenv("BAMBOO_SMTP_PASS"); v != "" {
		c.SMTP.Pass = v
	}
	if v := os.Getenv("BAMBOO_SMTP_FROM"); v != "" {
		c.SMTP.From = v
	}
	if v := os.Getenv("BAMBOO_SMTP_PUBLIC_BASE_URL"); v != "" {
		c.SMTP.PublicBaseURL = v
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
	// Release feed defaults; env overrides apply on top.
	if c.ReleaseFeed.Repo == "" {
		c.ReleaseFeed.Repo = "hanfour/bamboo"
	}
	if c.ReleaseFeed.Interval == 0 {
		c.ReleaseFeed.Interval = time.Hour
	}
	// Enabled defaults to true UNLESS the env explicitly opts out.
	c.ReleaseFeed.Enabled = true
	if v := os.Getenv("BAMBOO_RELEASE_FEED_ENABLED"); v != "" {
		c.ReleaseFeed.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("BAMBOO_RELEASE_FEED_REPO"); v != "" {
		c.ReleaseFeed.Repo = v
	}
	if v := os.Getenv("BAMBOO_RELEASE_FEED_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.ReleaseFeed.Interval = d
		}
	}
}

// validate enforces minimum required fields.
func (c *Config) validate() error {
	// Release feed: clamp interval to 5m floor + validate the repo
	// shape. A bad repo disables the feed entirely so the operator
	// gets "no badge" instead of "every fetch errors". Run first so
	// callers that only need the side-effects (tests, partial configs)
	// always get a normalised ReleaseFeed regardless of other errors.
	if c.ReleaseFeed.Enabled {
		if c.ReleaseFeed.Interval < 5*time.Minute {
			slog.Warn("release_feed: interval below 5m floor, clamping",
				"requested", c.ReleaseFeed.Interval, "floor", 5*time.Minute)
			c.ReleaseFeed.Interval = 5 * time.Minute
		}
		// Shape: exactly one "/", non-empty on both sides.
		parts := strings.Split(c.ReleaseFeed.Repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			slog.Warn("release_feed: invalid repo, disabling",
				"repo", c.ReleaseFeed.Repo)
			c.ReleaseFeed.Enabled = false
		}
	}
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
