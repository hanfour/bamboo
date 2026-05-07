// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// CoordinatorHandler implements bamboov1.CoordinatorServiceServer.
type CoordinatorHandler struct {
	bamboov1.UnimplementedCoordinatorServiceServer
}

func NewCoordinatorHandler() *CoordinatorHandler {
	return &CoordinatorHandler{}
}
