// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"context"
	"fmt"
)

// EnsureTayga makes the tayga binary available: a no-op if it is already
// on PATH, otherwise it detects the host package manager and installs it.
// Returns a clear error if no known package manager is present or the
// install fails — the caller (Up) surfaces it and leaves the egress
// non-translating until the next register retries.
func EnsureTayga(ctx context.Context, lookPath lookPathFn, run runner) error {
	if _, err := lookPath("tayga"); err == nil {
		return nil
	}
	pm, err := DetectPackageManager(lookPath)
	if err != nil {
		return err
	}
	name, args := installCmd(pm)
	if _, err := run(ctx, name, args...); err != nil {
		return fmt.Errorf("install tayga via %s: %w", pm.Name, err)
	}
	return nil
}
