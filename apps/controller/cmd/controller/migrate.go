// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/spf13/cobra"
)

var migrateConfigPath string

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run, roll back, or inspect database migrations",
	Long: `Apply, roll back, or inspect the controller's embedded migrations.

The migrations are baked into the binary; the running version is the source of
truth. The DSN is taken from the controller config (overridable by DATABASE_URL).`,
}

var migrateUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Apply all pending migrations",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := signalCtx()
		dsn, err := loadDSN(migrateConfigPath)
		if err != nil {
			return err
		}
		return db.MigrateUp(ctx, dsn)
	},
}

var migrateDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Roll back the most recently applied migration",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := signalCtx()
		dsn, err := loadDSN(migrateConfigPath)
		if err != nil {
			return err
		}
		return db.MigrateDown(ctx, dsn)
	},
}

var migrateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print migration status",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := signalCtx()
		dsn, err := loadDSN(migrateConfigPath)
		if err != nil {
			return err
		}
		return db.MigrateStatus(ctx, dsn)
	},
}

func init() {
	migrateCmd.PersistentFlags().StringVarP(&migrateConfigPath, "config", "c", "config/example.yaml", "path to YAML config file")
	migrateCmd.AddCommand(migrateUpCmd)
	migrateCmd.AddCommand(migrateDownCmd)
	migrateCmd.AddCommand(migrateStatusCmd)
}

func loadDSN(configPath string) (string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if cfg.Database.URL == "" {
		return "", fmt.Errorf("database.url not set")
	}
	return cfg.Database.URL, nil
}

func signalCtx() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}
