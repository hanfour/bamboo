// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	migrations "github.com/hanfour/bamboo/apps/controller/migrations"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registers itself with database/sql under "pgx"
	"github.com/pressly/goose/v3"
)

// MigrateUp applies every pending migration to the database identified by dsn.
// It uses the controller's embedded migrations FS, so the running binary
// always carries the migrations it was built with.
func MigrateUp(ctx context.Context, dsn string) error {
	return runGoose(ctx, dsn, func(db *sql.DB) error {
		return goose.UpContext(ctx, db, ".")
	})
}

// MigrateDown rolls back the most recently applied migration.
func MigrateDown(ctx context.Context, dsn string) error {
	return runGoose(ctx, dsn, func(db *sql.DB) error {
		return goose.DownContext(ctx, db, ".")
	})
}

// MigrateStatus prints the migration status to goose's logger.
func MigrateStatus(ctx context.Context, dsn string) error {
	return runGoose(ctx, dsn, func(db *sql.DB) error {
		return goose.StatusContext(ctx, db, ".")
	})
}

func runGoose(_ context.Context, dsn string, op func(*sql.DB) error) error {
	sqlDB, err := openStdlib(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	goose.SetBaseFS(fs.FS(migrations.FS))
	defer goose.SetBaseFS(nil)

	return op(sqlDB)
}

func openStdlib(dsn string) (*sql.DB, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return sqlDB, nil
}
