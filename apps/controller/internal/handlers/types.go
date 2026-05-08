// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import "context"

// ctxAlias keeps gRPC handler signatures tidy: receiver methods can
// declare `_ ctxAlias` to satisfy the generated interface without
// importing context everywhere just for the type name.
type ctxAlias = context.Context
