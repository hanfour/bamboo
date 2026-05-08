// SPDX-License-Identifier: AGPL-3.0-or-later

// Package migrations holds the controller's SQL migrations and exposes
// them as an embed.FS so the binary can run them at startup.
//
// The migrations themselves live alongside this file as `*.sql` and remain
// invokable from the goose CLI (`goose -dir apps/controller/migrations ...`).
package migrations

import "embed"

// FS contains all *.sql files in this directory.
//
//go:embed *.sql
var FS embed.FS
