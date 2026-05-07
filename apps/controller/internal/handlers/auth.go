// SPDX-License-Identifier: AGPL-3.0-or-later

// Package handlers contains gRPC service implementations.
//
// During the bootstrap phase each handler embeds the upstream
// Unimplemented*Server to satisfy the interface; concrete logic lands as
// individual Sprint 1+ tickets close.
package handlers

import (
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// AuthHandler implements bamboov1.AuthServiceServer.
type AuthHandler struct {
	bamboov1.UnimplementedAuthServiceServer
}

// NewAuthHandler constructs a fresh AuthHandler. It is stateless and safe
// to share across goroutines.
func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}
