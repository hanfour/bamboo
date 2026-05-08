// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repo holds the controller's data-access layer. Each table gets
// its own repository struct. Repositories are stateless apart from the
// shared *db.Pool.
package repo

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a repository lookup yields no row.
var ErrNotFound = errors.New("not found")

// asNotFound maps pgx.ErrNoRows to our domain error so callers do not need
// to import pgx for error inspection.
func asNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// errIs is a thin alias around errors.Is to keep imports out of leaf files.
func errIs(err, target error) bool {
	return errors.Is(err, target)
}
