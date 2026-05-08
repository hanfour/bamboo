// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// PolicyHandler implements bamboov1.PolicyServiceServer.
type PolicyHandler struct {
	bamboov1.UnimplementedPolicyServiceServer
}

// NewPolicyHandler constructs a fresh PolicyHandler.
func NewPolicyHandler() *PolicyHandler {
	return &PolicyHandler{}
}
