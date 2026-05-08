// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TelemetryHandler implements bamboov1.TelemetryServiceServer.
type TelemetryHandler struct {
	bamboov1.UnimplementedTelemetryServiceServer
}

// NewTelemetryHandler constructs a fresh TelemetryHandler.
func NewTelemetryHandler() *TelemetryHandler {
	return &TelemetryHandler{}
}
