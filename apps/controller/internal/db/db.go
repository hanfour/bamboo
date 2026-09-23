// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db wires the controller's Postgres connection pool and exposes
// migration helpers.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is a typed alias used throughout the controller to keep the dep
// surface narrow.
type Pool = pgxpool.Pool

// Open creates a connection pool and verifies connectivity with a ping.
// Callers should defer pool.Close().
//
// When migration 00022 has created bamboo_app, every connection assumes
// that role. Superusers bypass RLS even under FORCE ROW LEVEL SECURITY;
// SET ROLE bamboo_app is what makes the policies apply to a superuser
// DSN (local compose, CI). Databases that have not migrated yet keep
// the login role so older test databases still open.
func Open(ctx context.Context, dsn string) (*Pool, error) {
	return open(ctx, dsn, "bamboo_app")
}

// OpenMaintenance is the pool for tests and tools that seed or inspect
// every tenant. It assumes bamboo_maintenance (BYPASSRLS) when that
// role exists. The running controller does not use this; cross-tenant
// work in-process goes through WithBypass on an Open pool.
func OpenMaintenance(ctx context.Context, dsn string) (*Pool, error) {
	return open(ctx, dsn, "bamboo_maintenance")
}

func open(ctx context.Context, dsn, role string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	// Conservative defaults; tune as load is measured.
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return assumeRole(ctx, conn, role)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	return pool, nil
}

// assumeRole switches the connection to role when that role exists.
// The SQL is a fixed literal per known role; role is not interpolated
// from outside this package.
func assumeRole(ctx context.Context, conn *pgx.Conn, role string) error {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("lookup role %s: %w", role, err)
	}
	if !exists {
		return nil
	}
	var stmt string
	switch role {
	case "bamboo_app":
		stmt = `SET ROLE bamboo_app`
	case "bamboo_maintenance":
		stmt = `SET ROLE bamboo_maintenance`
	default:
		return fmt.Errorf("unknown pool role %q", role)
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("set role %s: %w", role, err)
	}
	return nil
}
