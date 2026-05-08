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
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	Auth     AuthConfig     `yaml:"auth"`
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

// AuthConfig holds OIDC provider credentials.
type AuthConfig struct {
	OIDC OIDCProviders `yaml:"oidc"`
}

// OIDCProviders bundles per-provider credentials.
type OIDCProviders struct {
	Google ClientCredentials `yaml:"google"`
	GitHub ClientCredentials `yaml:"github"`
}

// ClientCredentials is a generic OAuth client_id / client_secret pair.
type ClientCredentials struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// Load reads the YAML at path, applies environment overrides, and validates.
func Load(path string) (*Config, error) {
	// path is operator-supplied via --config flag; intentional file inclusion.
	raw, err := os.ReadFile(path) //nolint:gosec // G304: trusted operator input
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg.applyEnvOverrides()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
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
}

// validate enforces minimum required fields.
func (c *Config) validate() error {
	if c.Server.GRPCAddr == "" {
		return fmt.Errorf("server.grpc_addr is required")
	}
	if c.Database.URL == "" {
		return fmt.Errorf("database.url is required (or set DATABASE_URL)")
	}
	return nil
}
